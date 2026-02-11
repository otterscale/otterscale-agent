package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pb "github.com/otterscale/otterscale-agent/api/resource/v1"
	"github.com/otterscale/otterscale-agent/api/resource/v1/pbconnect"
	"github.com/otterscale/otterscale-agent/internal/app"
	"github.com/otterscale/otterscale-agent/internal/cmd"
	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/identity"
	"github.com/otterscale/otterscale-agent/internal/mux"
	"github.com/otterscale/otterscale-agent/internal/providers/chisel"
	"github.com/otterscale/otterscale-agent/internal/providers/kubernetes"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// freePort allocates a free TCP port on 127.0.0.1 and returns it.
// There is a small race between close and reuse, which is acceptable in tests.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// waitForPort polls a TCP address until it accepts connections or the deadline
// is reached.
func waitForPort(t *testing.T, address string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("waitForPort: %s did not become available within %s", address, timeout)
}

// ---------------------------------------------------------------------------
// Fake Kubernetes API Server
// ---------------------------------------------------------------------------

// newFakeK8sServer returns an httptest.Server that mimics the minimum K8s API
// surface required by the DiscoveryClient.LookupResource and ResourceRepo.List
// code paths.
func newFakeK8sServer(t *testing.T) *httptest.Server {
	t.Helper()

	handler := http.NewServeMux()

	// Discovery: GET /api/v1 -> APIResourceList
	handler.HandleFunc("GET /api/v1", func(w http.ResponseWriter, r *http.Request) {
		// Verify impersonation headers are forwarded through the tunnel.
		if user := r.Header.Get("Impersonate-User"); user == "" {
			t.Error("fake k8s: expected Impersonate-User header, got empty")
		}

		resp := metav1.APIResourceList{
			TypeMeta: metav1.TypeMeta{
				Kind:       "APIResourceList",
				APIVersion: "v1",
			},
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{
					Name:       "pods",
					Kind:       "Pod",
					Namespaced: true,
					Verbs:      metav1.Verbs{"get", "list", "create", "delete", "watch"},
				},
				{
					Name:       "namespaces",
					Kind:       "Namespace",
					Namespaced: false,
					Verbs:      metav1.Verbs{"get", "list"},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("fake k8s: encode APIResourceList: %v", err)
		}
	})

	// List pods: GET /api/v1/namespaces/default/pods -> PodList
	handler.HandleFunc("GET /api/v1/namespaces/default/pods", func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"apiVersion": "v1",
			"kind":       "PodList",
			"metadata": map[string]any{
				"resourceVersion": "1000",
			},
			"items": []any{
				map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name":      "test-pod",
						"namespace": "default",
					},
					"spec": map[string]any{
						"containers": []any{
							map[string]any{
								"name":  "nginx",
								"image": "nginx:latest",
							},
						},
					},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("fake k8s: encode PodList: %v", err)
		}
	})

	// Catch-all for unexpected requests (helps debugging).
	handler.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Logf("fake k8s: unhandled request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// ---------------------------------------------------------------------------
// Test Auth Interceptor (replaces Keycloak OIDC interceptor)
// ---------------------------------------------------------------------------

type testAuthInterceptor struct {
	userInfo identity.UserInfo
}

func (i *testAuthInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx = identity.WithUserInfo(ctx, i.userInfo)
		return next(ctx, req)
	}
}

func (i *testAuthInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *testAuthInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx = identity.WithUserInfo(ctx, i.userInfo)
		return next(ctx, conn)
	}
}

// ---------------------------------------------------------------------------
// Integration Test
// ---------------------------------------------------------------------------

func TestTunnelListPods(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ---- 1. Start fake K8s API server ----
	fakeK8s := newFakeK8sServer(t)
	t.Logf("Fake K8s API server on %s", fakeK8s.URL)

	// ---- 2. Build dependencies (same wiring as wire_gen.go) ----
	t.Setenv("OTTERSCALE_TUNNEL_SERVER_KEY_SEED", "test-seed")

	conf := config.New()
	tunnel, err := chisel.NewChiselService(conf)
	if err != nil {
		t.Fatalf("NewChiselService: %v", err)
	}

	// Register the test cluster before the server starts the tunnel listener.
	// RegisterCluster adds the user to the chisel server and records the
	// cluster -> tunnel port mapping used by GetTunnelAddress.
	tunnelPort := freePort(t)
	if err := tunnel.RegisterCluster("test-cluster", "agent", "secret", tunnelPort); err != nil {
		t.Fatalf("RegisterCluster: %v", err)
	}
	t.Logf("Registered cluster 'test-cluster' with tunnel port %d", tunnelPort)

	k8s := kubernetes.New(tunnel)
	discoveryClient := kubernetes.NewDiscoveryClient(k8s)
	resourceRepo := kubernetes.NewResourceRepo(k8s)
	resourceUseCase := core.NewResourceUseCase(discoveryClient, resourceRepo)
	resourceService := app.NewResourceService(resourceUseCase)
	hub := mux.NewHub(resourceService)

	// ---- 3. Create and run the server command ----
	authInterceptor := &testAuthInterceptor{
		userInfo: identity.UserInfo{
			Subject: "test-user",
			Groups:  []string{"system:authenticated"},
		},
	}

	serverAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	tunnelAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))

	serverCmd := cmd.NewServer(conf, hub, tunnel, cmd.WithInterceptors(authInterceptor))
	serverCmd.SetArgs([]string{
		"--address", serverAddr,
		"--tunnel-address", tunnelAddr,
	})

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- serverCmd.ExecuteContext(ctx)
	}()

	// Wait for both the HTTP server and the tunnel server to be accepting connections.
	waitForPort(t, serverAddr, 5*time.Second)
	waitForPort(t, tunnelAddr, 5*time.Second)
	t.Logf("Server ready: HTTP on %s, tunnel on %s", serverAddr, tunnelAddr)

	// ---- 4. Create and run the agent command ----
	fingerprint := tunnel.GetFingerprint()

	agentCmd := cmd.NewAgent()
	agentCmd.SetArgs([]string{
		"--cluster", "test-cluster",
		"--server", fmt.Sprintf("http://%s", tunnelAddr),
		"--auth", "agent:secret",
		"--fingerprint", fingerprint,
		"--tunnel-port", fmt.Sprintf("%d", tunnelPort),
		"--kube-api-url", fakeK8s.URL,
	})

	agentErrCh := make(chan error, 1)
	go func() {
		agentErrCh <- agentCmd.ExecuteContext(ctx)
	}()

	// Wait for the reverse tunnel to be ready (the agent binds tunnelPort on
	// the server side through chisel).
	waitForPort(t, fmt.Sprintf("127.0.0.1:%d", tunnelPort), 5*time.Second)
	t.Log("Agent connected, reverse tunnel is ready")

	// ---- 5. Create Connect RPC client and call List ----
	client := pbconnect.NewResourceServiceClient(http.DefaultClient, fmt.Sprintf("http://%s", serverAddr))

	listReq := &pb.ListRequest{}
	listReq.SetCluster("test-cluster")
	listReq.SetGroup("")
	listReq.SetVersion("v1")
	listReq.SetResource("pods")
	listReq.SetNamespace("default")

	listResp, err := client.List(ctx, listReq)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	// ---- 6. Assertions ----
	items := listResp.GetItems()
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	obj := items[0].GetObject()
	if obj == nil {
		t.Fatal("expected non-nil object in first item")
	}

	metadata := obj.GetFields()["metadata"]
	if metadata == nil {
		t.Fatal("expected metadata field in object")
	}

	metadataFields := metadata.GetStructValue().GetFields()
	name := metadataFields["name"].GetStringValue()
	namespace := metadataFields["namespace"].GetStringValue()

	if name != "test-pod" {
		t.Errorf("expected pod name 'test-pod', got %q", name)
	}
	if namespace != "default" {
		t.Errorf("expected pod namespace 'default', got %q", namespace)
	}

	kind := obj.GetFields()["kind"].GetStringValue()
	if kind != "Pod" {
		t.Errorf("expected kind 'Pod', got %q", kind)
	}

	rv := listResp.GetResourceVersion()
	if !strings.Contains(rv, "1000") {
		t.Errorf("expected resourceVersion containing '1000', got %q", rv)
	}

	t.Log("All assertions passed: client -> server cmd -> tunnel -> agent cmd -> fake K8s -> response verified")

	// ---- 7. Shutdown ----
	cancel()

	select {
	case err := <-serverErrCh:
		if err != nil {
			t.Logf("Server exited with: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Log("Server did not exit within 5s (non-fatal)")
	}

	select {
	case err := <-agentErrCh:
		if err != nil {
			t.Logf("Agent exited with: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Log("Agent did not exit within 5s (non-fatal)")
	}
}
