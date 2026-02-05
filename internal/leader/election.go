package leader

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	coordinationv1 "k8s.io/client-go/kubernetes/typed/coordination/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// Elector runs Kubernetes Lease leader election.
// Intended for server-side HA gating.
type Elector struct {
	namespace string
	leaseName string
	identity  string

	isLeader atomic.Bool

	coreClient  corev1.CoreV1Interface
	coordClient coordinationv1.CoordinationV1Interface
}

type Config struct {
	// Namespace where the Lease object lives. If empty, it will be detected.
	Namespace string
	// LeaseName is the name of the Lease object.
	LeaseName string
	// Identity is the unique identity for this participant. If empty, it will be detected.
	Identity string

	// LeaseDuration is the duration that non-leader candidates will wait to force acquire leadership.
	LeaseDuration time.Duration
	// RenewDeadline is the duration that the acting leader will retry refreshing leadership before giving up.
	RenewDeadline time.Duration
	// RetryPeriod is the duration the LeaderElector clients should wait between tries.
	RetryPeriod time.Duration

	// Kubeconfig is an optional kubeconfig path for local/dev.
	Kubeconfig string
}

func NewElector(cfg Config) (*Elector, error) {
	ns := cfg.Namespace
	if ns == "" {
		ns = detectNamespace()
	}
	if ns == "" {
		return nil, fmt.Errorf("unable to detect namespace; set POD_NAMESPACE or mount serviceaccount namespace")
	}

	leaseName := cfg.LeaseName
	if leaseName == "" {
		leaseName = "otterscale-server-leader"
	}

	identity := cfg.Identity
	if identity == "" {
		identity = detectIdentity()
	}
	if identity == "" {
		return nil, fmt.Errorf("unable to detect identity; set POD_NAME or hostname")
	}

	restCfg, err := buildRestConfig(cfg.Kubeconfig)
	if err != nil {
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}

	return &Elector{
		namespace:   ns,
		leaseName:   leaseName,
		identity:    identity,
		coreClient:  clientset.CoreV1(),
		coordClient: clientset.CoordinationV1(),
	}, nil
}

func (e *Elector) IsLeader() bool {
	return e.isLeader.Load()
}

func (e *Elector) Identity() string {
	return e.identity
}

// LeaderPodName returns the current lease holder identity.
// In our deployment, we expect this to equal the leader pod name (POD_NAME).
func (e *Elector) LeaderPodName(ctx context.Context) (string, error) {
	lease, err := e.coordClient.Leases(e.namespace).Get(ctx, e.leaseName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if lease.Spec.HolderIdentity == nil || strings.TrimSpace(*lease.Spec.HolderIdentity) == "" {
		return "", errors.New("lease has no holder identity yet")
	}
	return strings.TrimSpace(*lease.Spec.HolderIdentity), nil
}

// LeaderPodIP resolves the current leader pod IP by reading the Lease holder identity
// and then fetching the corresponding Pod.
func (e *Elector) LeaderPodIP(ctx context.Context) (string, error) {
	name, err := e.LeaderPodName(ctx)
	if err != nil {
		return "", err
	}

	pod, err := e.coreClient.Pods(e.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(pod.Status.PodIP)
	if ip == "" {
		return "", fmt.Errorf("leader pod %q has empty podIP", name)
	}
	return ip, nil
}

// Run blocks until ctx is done. It will call callbacks on leadership changes.
// The returned error is only for setup/lock creation failures.
func (e *Elector) Run(ctx context.Context, onStartedLeading func(context.Context), onStoppedLeading func()) error {
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      e.leaseName,
			Namespace: e.namespace,
		},
		Client: e.coordClient,
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: e.identity,
		},
	}

	leaseDuration := 15 * time.Second
	renewDeadline := 10 * time.Second
	retryPeriod := 2 * time.Second

	lec := leaderelection.LeaderElectionConfig{
		Lock:          lock,
		LeaseDuration: leaseDuration,
		RenewDeadline: renewDeadline,
		RetryPeriod:   retryPeriod,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(c context.Context) {
				e.isLeader.Store(true)
				onStartedLeading(c)
			},
			OnStoppedLeading: func() {
				e.isLeader.Store(false)
				onStoppedLeading()
			},
		},
		ReleaseOnCancel: true,
		Name:            "otterscale",
	}

	le, err := leaderelection.NewLeaderElector(lec)
	if err != nil {
		return err
	}

	le.Run(ctx) // blocks
	return nil
}

func buildRestConfig(kubeconfig string) (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	if kubeconfig == "" {
		kubeconfig = os.Getenv("KUBECONFIG")
	}
	if kubeconfig == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			kubeconfig = home + "/.kube/config"
		}
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

func detectNamespace() string {
	if ns := strings.TrimSpace(os.Getenv("POD_NAMESPACE")); ns != "" {
		return ns
	}
	// Standard location in Kubernetes pods
	if b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

func detectIdentity() string {
	if n := strings.TrimSpace(os.Getenv("POD_NAME")); n != "" {
		return n
	}
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		return strings.TrimSpace(h) + "-" + shortRandom()
	}
	return shortRandom()
}

func shortRandom() string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf) // best-effort
	return base64.RawStdEncoding.EncodeToString(buf)
}
