package k8s

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func NewClient(kubeContext string) (kubernetes.Interface, error) {
	config, err := rest.InClusterConfig()
	if err == nil {
		return kubernetes.NewForConfig(config)
	}

	fmt.Println(kubeContext)
	fmt.Println("CONFIG ", config)

	// kubeconfig := defaultKubeconfigPath()
	kubeconfig := "<MANUAL_TEST>"
	fmt.Println(kubeconfig)

	loadingRules := &clientcmd.ClientConfigLoadingRules{
		ExplicitPath: kubeconfig,
	}
	overrides := &clientcmd.ConfigOverrides{}
	if kubeContext != "" {
		overrides.CurrentContext = kubeContext
	}

	config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("FAILED to build kubeconfig: %w", err)
	}

	// Increase QPS for event watching (default is very low)
	config.QPS = 50
	config.Burst = 100

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	return client, nil
}

func defaultKubeconfigPath() string {
	if env := os.Getenv("KUBECONFIG"); env != "" {
		return env
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".kube", "config")
}
