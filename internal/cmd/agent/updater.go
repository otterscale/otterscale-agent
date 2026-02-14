package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/Masterminds/semver/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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
	mu     sync.Mutex
	client kubernetes.Interface // cached clientset
	cfg    *rest.Config
	log    *slog.Logger
}

// newUpdater returns an updater configured with the Deployment
// coordinates. Values are read from environment variables with
// sensible defaults.
func newUpdater(cfg *rest.Config) *updater {
	return &updater{
		cfg: cfg,
		log: slog.Default().With("component", "updater"),
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
// The version string is validated as semver to prevent arbitrary
// image tag injection from a compromised server.
func (u *updater) patch(ctx context.Context, version string) error {
	// Validate the version is a legitimate semver tag to prevent
	// arbitrary image injection (e.g. "latest@sha256:malicious...").
	if _, err := semver.StrictNewVersion(strings.TrimPrefix(version, "v")); err != nil {
		return fmt.Errorf("invalid server version %q: %w", version, err)
	}

	client, err := u.getOrCreateClient()
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
							Name:  containerName,
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

	namespace := detectNamespace()

	u.log.Info("patching agent deployment",
		"deployment", deploymentName,
		"namespace", namespace,
		"image", image,
	)

	_, err = client.AppsV1().Deployments(namespace).Patch(
		ctx,
		deploymentName,
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

// getOrCreateClient returns the cached Kubernetes clientset, creating
// it on first use. The clientset is reused across patch calls to avoid
// redundant connection setup. Access is serialised by mu to prevent
// data races if multiple registrations overlap.
func (u *updater) getOrCreateClient() (kubernetes.Interface, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.client != nil {
		return u.client, nil
	}

	client, err := kubernetes.NewForConfig(u.cfg)
	if err != nil {
		return nil, err
	}
	u.client = client
	return client, nil
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
