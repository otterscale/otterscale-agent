package kubernetes

import (
	"context"
	"fmt"
	"os"

	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/otterscale/otterscale-agent/internal/impersonation"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Kubernetes struct {
	conf *config.Config
}

func New(conf *config.Config) *Kubernetes {
	return &Kubernetes{
		conf: conf,
	}
}

func (m *Kubernetes) tunnel(cluster string) (*rest.Config, error) {
	// Agent-side execution model:
	// - typically runs inside the cluster, so use in-cluster service account
	// - for local development, fall back to kubeconfig if available
	_ = cluster // reserved for future multi-cluster agent support

	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}

	// Fallback: use KUBECONFIG or default path.
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			kubeconfig = home + "/.kube/config"
		}
	}

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build kube config: %w", err)
	}
	return cfg, nil
}

func (m *Kubernetes) dynamic(ctx context.Context, cluster string) (*dynamic.DynamicClient, error) {
	userSub, ok := impersonation.GetSubject(ctx)
	if !ok {
		return nil, fmt.Errorf("user sub not found in context")
	}

	config, err := m.tunnel(cluster)
	if err != nil {
		return nil, err
	}

	userConfig := rest.CopyConfig(config)

	userConfig.Impersonate = rest.ImpersonationConfig{
		UserName: userSub,
		Groups:   []string{"system:authenticated"},
	}

	return dynamic.NewForConfig(userConfig)
}

func (m *Kubernetes) discovery(cluster string) (*discovery.DiscoveryClient, error) {
	config, err := m.tunnel(cluster)
	if err != nil {
		return nil, err
	}

	return discovery.NewDiscoveryClientForConfig(config)
}
