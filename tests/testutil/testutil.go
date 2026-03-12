//go:build integration

// Package testutil provides shared test helpers for vibeD integration tests.
package testutil

import (
	"context"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/k8s"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// MustGetClients creates Kubernetes clients from the default kubeconfig.
// It calls t.Fatal if the cluster is unreachable.
func MustGetClients(t *testing.T) *k8s.Clients {
	t.Helper()
	clients, err := k8s.NewClients(config.KubernetesConfig{})
	if err != nil {
		t.Fatalf("cannot connect to Kubernetes cluster: %v", err)
	}
	return clients
}

// SkipIfNoCluster skips the test if no Kubernetes cluster is available.
func SkipIfNoCluster(t *testing.T) {
	t.Helper()
	clients, err := k8s.NewClients(config.KubernetesConfig{})
	if err != nil {
		t.Skipf("skipping: no Kubernetes cluster available: %v", err)
		return
	}
	// Verify cluster is reachable
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = clients.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		t.Skipf("skipping: Kubernetes cluster unreachable: %v", err)
	}
}

// CreateTestNamespace creates a unique test namespace and registers cleanup.
// The namespace is labeled with vibed-test=true for manual cleanup via
// `kubectl delete ns -l vibed-test=true`.
func CreateTestNamespace(t *testing.T, clientset kubernetes.Interface) string {
	t.Helper()
	name := fmt.Sprintf("vibed-test-%s", randomSuffix())

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"vibed-test": "true",
			},
		},
	}

	ctx := context.Background()
	_, err := clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating test namespace %q: %v", name, err)
	}

	t.Cleanup(func() {
		err := clientset.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil {
			t.Logf("warning: failed to delete test namespace %q: %v", name, err)
		}
	})

	return name
}

// WaitForCondition polls until the condition function returns true or the timeout expires.
func WaitForCondition(t *testing.T, timeout, poll time.Duration, condition func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(poll)
	}
	t.Fatalf("timed out waiting for condition: %s", msg)
}

// WaitForDeploymentReady waits until a Deployment has at least one ready replica.
func WaitForDeploymentReady(t *testing.T, clientset kubernetes.Interface, ns, name string, timeout time.Duration) {
	t.Helper()
	WaitForCondition(t, timeout, 2*time.Second, func() bool {
		dep, err := clientset.AppsV1().Deployments(ns).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		return dep.Status.ReadyReplicas >= 1
	}, fmt.Sprintf("deployment %s/%s to have ready replicas", ns, name))
}

// WaitForPodRunning waits until at least one pod matching the label selector is Running.
func WaitForPodRunning(t *testing.T, clientset kubernetes.Interface, ns, labelSelector string, timeout time.Duration) {
	t.Helper()
	WaitForCondition(t, timeout, 2*time.Second, func() bool {
		pods, err := clientset.CoreV1().Pods(ns).List(context.Background(), metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err != nil || len(pods.Items) == 0 {
			return false
		}
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				return true
			}
		}
		return false
	}, fmt.Sprintf("pod with labels %q in %s to be Running", labelSelector, ns))
}

// TestConfig returns a Config suitable for integration testing.
// It uses in-memory store, local storage at tmpDir, and the given namespace.
func TestConfig(ns, tmpDir string) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Transport: "http",
			HTTPAddr:  ":0", // random port
		},
		Deployment: config.DeploymentConfig{
			PreferredTarget: "kubernetes",
			Namespace:       ns,
		},
		Builder: config.BuilderConfig{
			Image:      "paketobuildpacks/builder-jammy-base:latest",
			PullPolicy: "if-not-present",
		},
		Storage: config.StorageConfig{
			Backend: "local",
			Local: config.LocalStorageConfig{
				BasePath: tmpDir,
			},
		},
		Registry: config.RegistryConfig{
			Enabled: false,
		},
		Store: config.StoreConfig{
			Backend: "memory",
		},
		Knative: config.KnativeConfig{
			DomainSuffix: "127.0.0.1.sslip.io",
			IngressClass: "kourier.ingress.networking.knative.dev",
		},
	}
}

// RandomName returns a DNS-safe random name for test artifacts.
func RandomName() string {
	return fmt.Sprintf("test-app-%s", randomSuffix())
}

func randomSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
