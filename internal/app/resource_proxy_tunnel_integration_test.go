package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"connectrpc.com/connect"
	chclient "github.com/jpillora/chisel/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	pb "github.com/otterscale/otterscale-agent/api/resource/v1"
	"github.com/otterscale/otterscale-agent/api/resource/v1/pbconnect"
	chiseltunnel "github.com/otterscale/otterscale-agent/internal/chisel"
	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/otterscale/otterscale-agent/internal/impersonation"
)

type headerCaptureInterceptor struct {
	mu     sync.Mutex
	latest string
}

func (i *headerCaptureInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		i.mu.Lock()
		i.latest = req.Header().Get("X-Otterscale-Subject")
		i.mu.Unlock()
		return next(ctx, req)
	}
}

func (i *headerCaptureInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *headerCaptureInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func (i *headerCaptureInterceptor) Latest() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.latest
}

type subjectAssertingService struct {
	pbconnect.UnimplementedResourceServiceHandler

	mu     sync.Mutex
	latest string
}

func (s *subjectAssertingService) List(ctx context.Context, _ *pb.ListRequest) (*pb.ListResponse, error) {
	sub, ok := impersonation.GetSubject(ctx)
	if !ok || sub == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("subject missing from context"))
	}
	s.mu.Lock()
	s.latest = sub
	s.mu.Unlock()
	return &pb.ListResponse{}, nil
}

func (s *subjectAssertingService) Latest() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest
}

func freeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

var _ = Describe("ResourceProxy tunnel forwarding", func() {
	It("forwards requests over chisel tunnel and propagates trusted subject header", func(ctx SpecContext) {
		const (
			cluster  = "dev"
			user     = "agent-user"
			pass     = "agent-pass"
			subject  = "user-subject-123"
			keySeed  = "test-seed"
			waitTime = 5 * time.Second
		)

		chiselPort, err := freeTCPPort()
		Expect(err).NotTo(HaveOccurred())

		tunnelPort, err := freeTCPPort()
		Expect(err).NotTo(HaveOccurred())

		// Start agent HTTP server on a real listener so we can wire chisel to it.
		agentListener, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		agentPort := agentListener.Addr().(*net.TCPAddr).Port

		conf := config.New()

		tempDir := GinkgoT().TempDir()
		cfgPath := filepath.Join(tempDir, "otterscale.yaml")
		cfg := fmt.Sprintf(
			`tunnel:
  server:
    host: 127.0.0.1
    port: %q
    key_seed: %q
clusters:
  %s:
    agent:
      auth:
        user: %q
        pass: %q
      tunnel_port: %d
      api_port: %d
`,
			fmt.Sprintf("%d", chiselPort),
			keySeed,
			cluster,
			user,
			pass,
			tunnelPort,
			agentPort,
		)
		Expect(os.WriteFile(cfgPath, []byte(cfg), 0o600)).To(Succeed())
		Expect(conf.Load(cfgPath)).To(Succeed())

		tunnels := chiseltunnel.NewTunnelService(conf)
		Expect(tunnels.Start()).To(Succeed())
		DeferCleanup(func() {
			_ = tunnels.Stop()
		})

		// Agent handler: requires trusted subject header, then captures it in context + header.
		svc := &subjectAssertingService{}
		capture := &headerCaptureInterceptor{}
		trusted := impersonation.NewTrustedSubjectHeaderInterceptor()
		path, h := pbconnect.NewResourceServiceHandler(
			svc,
			connect.WithInterceptors(trusted, capture),
		)

		mux := http.NewServeMux()
		mux.Handle(path, h)

		agentServer := &http.Server{Handler: mux}
		go func() {
			_ = agentServer.Serve(agentListener)
		}()
		DeferCleanup(func() {
			_ = agentServer.Close()
		})

		// Start real chisel client to open the reverse port on the server.
		chiselCfg := &chclient.Config{
			Server:        conf.TunnelServerAddr(),
			Fingerprint:   tunnels.Fingerprint(),
			Auth:          fmt.Sprintf("%s:%s", user, pass),
			Remotes:       []string{fmt.Sprintf("R:127.0.0.1:%d:127.0.0.1:%d", tunnelPort, agentPort)},
			KeepAlive:     1 * time.Second,
			MaxRetryCount: -1,
		}
		client, err := chclient.NewClient(chiselCfg)
		Expect(err).NotTo(HaveOccurred())

		clientCtx, cancelClient := context.WithCancel(ctx)
		Expect(client.Start(clientCtx)).To(Succeed())
		waitDone := make(chan error, 1)
		go func() { waitDone <- client.Wait() }()
		DeferCleanup(func() {
			cancelClient()
			select {
			case <-time.After(2 * time.Second):
			case <-waitDone:
			}
		})

		// Ensure the reverse port is reachable before making the proxied call.
		_, err = tunnels.AgentBaseURL(cluster, waitTime)
		Expect(err).NotTo(HaveOccurred())

		proxy := NewResourceProxy(conf, tunnels, nil)

		req := &pb.ListRequest{}
		req.SetCluster(cluster)

		By("success path: subject in context is propagated over tunnel")
		resp, err := proxy.List(impersonation.WithSubject(context.Background(), subject), req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp).NotTo(BeNil())
		Eventually(svc.Latest, 2*time.Second).Should(Equal(subject))
		Eventually(capture.Latest, 2*time.Second).Should(Equal(subject))

		By("negative path: missing subject is rejected before forwarding")
		_, err = proxy.List(context.Background(), req)
		Expect(err).To(HaveOccurred())
		var ce *connect.Error
		Expect(errors.As(err, &ce)).To(BeTrue())
		Expect(ce.Code()).To(Equal(connect.CodeUnauthenticated))
	})
})
