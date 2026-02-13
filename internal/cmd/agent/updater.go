package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// imageRepo is the fixed container image repository shared by both
	// the server and the agent. The tag is derived from the version.
	imageRepo = "ghcr.io/otterscale/otterscale"

	// containerName is the name of the container inside the Deployment to patch.
	containerName = "otterscale"

	// deploymentName is the default Kubernetes Deployment name used for the agent.
	deploymentName = "otterscale-agent"
)

// inClusterNamespacePath is the standard Kubernetes path that exposes
// the pod's namespace via the Downward API / service account mount.
const inClusterNamespacePath = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// updater performs in-cluster Deployment image patches so the agent
// can self-update to match the server version.
type updater struct {
	namespace      string
	deploymentName string
	containerName  string
	log            *slog.Logger
}

// newUpdater returns an updater configured with the Deployment
// coordinates. It returns nil if deploymentName is empty (self-update
// disabled). If namespace is empty it is auto-detected from the
// in-cluster service account.
func newUpdater() *updater {
	return &updater{
		namespace:      detectNamespace(),
		deploymentName: deploymentName,
		containerName:  containerName,
		log:            slog.Default().With("component", "updater"),
	}
}

// imageRef constructs the full image reference from the fixed repo
// and the given version tag.
func imageRef(version string) string {
	return imageRepo + ":" + version
}

// containerPatch is the minimal JSON structure for a strategic merge
// patch that updates a single container's image.
type containerPatch struct {
	Spec specPatch `json:"spec"`
}

type specPatch struct {
	Template templatePatch `json:"template"`
}

type templatePatch struct {
	Spec podSpecPatch `json:"spec"`
}

type podSpecPatch struct {
	Containers []containerImagePatch `json:"containers"`
}

type containerImagePatch struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

// patch updates the agent Deployment's container image to match the
// given version using a strategic merge patch. This preserves all
// other Deployment configuration (resources, env, volumes, etc.).
func (u *updater) patch(ctx context.Context, version string) error {
	client, err := u.kubeClient()
	if err != nil {
		return fmt.Errorf("create kube client: %w", err)
	}

	image := imageRef(version)

	p := containerPatch{
		Spec: specPatch{
			Template: templatePatch{
				Spec: podSpecPatch{
					Containers: []containerImagePatch{
						{
							Name:  u.containerName,
							Image: image,
						},
					},
				},
			},
		},
	}

	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal patch: %w", err)
	}

	u.log.Info("patching agent deployment",
		"deployment", u.deploymentName,
		"namespace", u.namespace,
		"image", image,
	)

	_, err = client.AppsV1().Deployments(u.namespace).Patch(
		ctx,
		u.deploymentName,
		types.StrategicMergePatchType,
		data,
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patch deployment: %w", err)
	}

	u.log.Info("agent deployment patched, rolling update will restart the agent")
	return nil
}

// kubeClient creates a Kubernetes clientset using the in-cluster
// config, falling back to KUBECONFIG.
func (u *updater) kubeClient() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cfg, err = clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		if err != nil {
			return nil, fmt.Errorf("load kube config: %w", err)
		}
	}
	return kubernetes.NewForConfig(cfg)
}

// detectNamespace reads the pod namespace from the standard in-cluster
// service account mount. It returns "default" if detection fails.
func detectNamespace() string {
	data, err := os.ReadFile(inClusterNamespacePath)
	if err != nil {
		return "default"
	}
	if ns := strings.TrimSpace(string(data)); ns != "" {
		return ns
	}
	return "default"
}
