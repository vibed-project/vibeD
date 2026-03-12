package k8s

import (
	"fmt"

	"github.com/vibed-project/vibeD/internal/config"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Clients holds all Kubernetes client variants needed by vibeD.
type Clients struct {
	Clientset     kubernetes.Interface
	DynamicClient dynamic.Interface
	Discovery     discovery.DiscoveryInterface
	RestConfig    *rest.Config
}

// NewClients initializes Kubernetes clients from config.
// If kubeconfig is empty, it tries in-cluster config.
func NewClients(cfg config.KubernetesConfig) (*Clients, error) {
	restConfig, err := buildRestConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building k8s rest config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("creating k8s clientset: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client: %w", err)
	}

	return &Clients{
		Clientset:     clientset,
		DynamicClient: dynClient,
		Discovery:     clientset.Discovery(),
		RestConfig:    restConfig,
	}, nil
}

func buildRestConfig(cfg config.KubernetesConfig) (*rest.Config, error) {
	if cfg.Kubeconfig != "" {
		return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: cfg.Kubeconfig},
			&clientcmd.ConfigOverrides{CurrentContext: cfg.Context},
		).ClientConfig()
	}

	// Try in-cluster first, fall back to default kubeconfig location.
	restConfig, err := rest.InClusterConfig()
	if err == nil {
		return restConfig, nil
	}

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{CurrentContext: cfg.Context},
	).ClientConfig()
}

// HasCRD checks if a specific CustomResourceDefinition exists in the cluster.
func HasCRD(disc discovery.DiscoveryInterface, group, version, resource string) (bool, error) {
	resources, err := disc.ServerResourcesForGroupVersion(group + "/" + version)
	if err != nil {
		// Group not found means the CRD doesn't exist.
		return false, nil
	}
	for _, r := range resources.APIResources {
		if r.Name == resource {
			return true, nil
		}
	}
	return false, nil
}
