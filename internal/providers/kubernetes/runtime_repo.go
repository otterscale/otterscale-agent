package kubernetes

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"connectrpc.com/authn"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/transport/spdy"

	"github.com/otterscale/otterscale-agent/internal/core"
)

// runtimeRepo implements core.RuntimeRepo by delegating to the
// Kubernetes typed, dynamic, and SPDY clients, accessed through
// the tunnel.
type runtimeRepo struct {
	kubernetes *Kubernetes
}

// NewRuntimeRepo returns a core.RuntimeRepo backed by Kubernetes.
func NewRuntimeRepo(kubernetes *Kubernetes) core.RuntimeRepo {
	return &runtimeRepo{kubernetes: kubernetes}
}

var _ core.RuntimeRepo = (*runtimeRepo)(nil)

// ---------------------------------------------------------------------------
// PodLogs
// ---------------------------------------------------------------------------

// PodLogs opens a streaming log reader for a container.
func (r *runtimeRepo) PodLogs(ctx context.Context, cluster, namespace, name string, opts core.PodLogOptions) (io.ReadCloser, error) {
	clientset, err := r.clientset(ctx, cluster)
	if err != nil {
		return nil, err
	}

	logOpts := &corev1.PodLogOptions{
		Container:  opts.Container,
		Follow:     opts.Follow,
		Previous:   opts.Previous,
		Timestamps: opts.Timestamps,
	}
	if opts.TailLines != nil {
		logOpts.TailLines = opts.TailLines
	}
	if opts.SinceSeconds != nil {
		logOpts.SinceSeconds = opts.SinceSeconds
	}
	if opts.SinceTime != nil {
		logOpts.SinceTime = &metav1.Time{Time: *opts.SinceTime}
	}
	if opts.LimitBytes != nil {
		logOpts.LimitBytes = opts.LimitBytes
	}

	return clientset.CoreV1().Pods(namespace).GetLogs(name, logOpts).Stream(ctx)
}

// ---------------------------------------------------------------------------
// Exec
// ---------------------------------------------------------------------------

// Exec starts an interactive exec session and blocks until it completes.
func (r *runtimeRepo) Exec(ctx context.Context, cluster, namespace, name string, opts core.ExecOptions) error {
	config, err := r.spdyConfig(ctx, cluster)
	if err != nil {
		return err
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	execOpts := &corev1.PodExecOptions{
		Container: opts.Container,
		Command:   opts.Command,
		TTY:       opts.TTY,
		Stdin:     opts.Stdin != nil,
		Stdout:    opts.Stdout != nil,
		Stderr:    opts.Stderr != nil,
	}

	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(name).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(execOpts, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(config, http.MethodPost, req.URL())
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	streamOpts := remotecommand.StreamOptions{
		Stdin:  opts.Stdin,
		Stdout: opts.Stdout,
		Stderr: opts.Stderr,
		Tty:    opts.TTY,
	}
	if opts.TTY && opts.SizeQueue != nil {
		streamOpts.TerminalSizeQueue = opts.SizeQueue
	}

	return executor.StreamWithContext(ctx, streamOpts)
}

// ---------------------------------------------------------------------------
// Scale
// ---------------------------------------------------------------------------

// GetScale reads the current replica count via the /scale subresource.
func (r *runtimeRepo) GetScale(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, name string) (int32, error) {
	client, err := r.dynamicClient(ctx, cluster)
	if err != nil {
		return 0, err
	}

	scaleObj, err := client.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{}, "scale")
	if err != nil {
		return 0, err
	}

	replicas, found, err := unstructured.NestedInt64(scaleObj.Object, "spec", "replicas")
	if err != nil || !found {
		return 0, apierrors.NewInternalError(fmt.Errorf("failed to read spec.replicas from scale subresource"))
	}

	return int32(replicas), nil
}

// UpdateScale sets the desired replica count via the /scale subresource.
func (r *runtimeRepo) UpdateScale(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, name string, replicas int32) (int32, error) {
	client, err := r.dynamicClient(ctx, cluster)
	if err != nil {
		return 0, err
	}

	// GET current scale
	scaleObj, err := client.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{}, "scale")
	if err != nil {
		return 0, err
	}

	// SET desired replicas
	if err := unstructured.SetNestedField(scaleObj.Object, int64(replicas), "spec", "replicas"); err != nil {
		return 0, apierrors.NewInternalError(err)
	}

	// UPDATE scale subresource
	updated, err := client.Resource(gvr).Namespace(namespace).Update(ctx, scaleObj, metav1.UpdateOptions{}, "scale")
	if err != nil {
		return 0, err
	}

	newReplicas, _, _ := unstructured.NestedInt64(updated.Object, "spec", "replicas")
	return int32(newReplicas), nil
}

// ---------------------------------------------------------------------------
// Restart
// ---------------------------------------------------------------------------

// Restart triggers a rolling restart by patching the pod template
// annotation with kubectl.kubernetes.io/restartedAt, equivalent to
// `kubectl rollout restart`.
func (r *runtimeRepo) Restart(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, name string) error {
	client, err := r.dynamicClient(ctx, cluster)
	if err != nil {
		return err
	}

	patch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"%s"}}}}}`,
		time.Now().UTC().Format(time.RFC3339),
	)

	_, err = client.Resource(gvr).Namespace(namespace).Patch(ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// ---------------------------------------------------------------------------
// PortForward
// ---------------------------------------------------------------------------

// PortForward opens a port-forward session via SPDY and copies data
// bidirectionally between the caller's stdin/stdout and the pod.
func (r *runtimeRepo) PortForward(ctx context.Context, cluster, namespace, name string, port int32, stdin io.Reader, stdout io.Writer) error {
	config, err := r.spdyConfig(ctx, cluster)
	if err != nil {
		return err
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(name).
		Namespace(namespace).
		SubResource("portforward")

	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, req.URL())
	streamConn, _, err := dialer.Dial(portForwardProtocolV1)
	if err != nil {
		return err
	}
	defer streamConn.Close()

	portStr := strconv.FormatInt(int64(port), 10)
	requestID := "0"

	// Create error stream.
	errorHeaders := http.Header{}
	errorHeaders.Set(corev1.StreamType, corev1.StreamTypeError)
	errorHeaders.Set(corev1.PortHeader, portStr)
	errorHeaders.Set(corev1.PortForwardRequestIDHeader, requestID)

	errorStream, err := streamConn.CreateStream(errorHeaders)
	if err != nil {
		return apierrors.NewInternalError(fmt.Errorf("create error stream: %w", err))
	}
	// Close the write direction of the error stream; we only read from it.
	defer errorStream.Close()

	// Create data stream.
	dataHeaders := http.Header{}
	dataHeaders.Set(corev1.StreamType, corev1.StreamTypeData)
	dataHeaders.Set(corev1.PortHeader, portStr)
	dataHeaders.Set(corev1.PortForwardRequestIDHeader, requestID)

	dataStream, err := streamConn.CreateStream(dataHeaders)
	if err != nil {
		return apierrors.NewInternalError(fmt.Errorf("create data stream: %w", err))
	}
	defer dataStream.Close()

	// Check for immediate errors.
	go func() {
		buf := make([]byte, 1024)
		n, _ := errorStream.Read(buf)
		if n > 0 {
			// Error from kubelet; the data stream will close shortly.
			_ = dataStream.Close()
		}
	}()

	// Bidirectional copy.
	errCh := make(chan error, 2)

	go func() {
		_, err := io.Copy(dataStream, stdin)
		errCh <- err
	}()

	go func() {
		_, err := io.Copy(stdout, dataStream)
		errCh <- err
	}()

	// Wait for either direction to complete or context cancellation.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// portForwardProtocolV1 is the subprotocol used for Kubernetes port
// forwarding over SPDY.
const portForwardProtocolV1 = "portforward.k8s.io"

// ---------------------------------------------------------------------------
// Client helpers
// ---------------------------------------------------------------------------

// clientset builds an impersonated typed Kubernetes clientset for the
// given cluster. Uses the pre-cached transport from Kubernetes.
func (r *runtimeRepo) clientset(ctx context.Context, cluster string) (*kubernetes.Clientset, error) {
	config, err := r.kubernetes.impersonationConfig(ctx, cluster)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	return cs, nil
}

// dynamicClient builds an impersonated dynamic client for the given
// cluster.
func (r *runtimeRepo) dynamicClient(ctx context.Context, cluster string) (*dynamic.DynamicClient, error) {
	config, err := r.kubernetes.impersonationConfig(ctx, cluster)
	if err != nil {
		return nil, err
	}
	return dynamic.NewForConfig(config)
}

// spdyConfig builds a rest.Config suitable for SPDY connections
// (exec, port-forward). Unlike impersonationConfig, it does NOT
// set a pre-built Transport because SPDY executors and dialers need
// to negotiate their own connection upgrade.
func (r *runtimeRepo) spdyConfig(ctx context.Context, cluster string) (*rest.Config, error) {
	userInfo, ok := authn.GetInfo(ctx).(core.UserInfo)
	if !ok {
		return nil, apierrors.NewUnauthorized("user info not found in context")
	}

	address, err := r.kubernetes.tunnel.ResolveAddress(cluster)
	if err != nil {
		return nil, apierrors.NewServiceUnavailable(err.Error())
	}

	return &rest.Config{
		Host: address,
		Impersonate: rest.ImpersonationConfig{
			UserName: userInfo.Subject,
			Groups:   userInfo.Groups,
		},
	}, nil
}

// Ensure httpstream.Connection is assignable (compile-time check).
var _ httpstream.Connection = (httpstream.Connection)(nil)
