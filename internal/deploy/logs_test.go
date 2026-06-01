package deploy

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// logsService wires a deploy.Service with a controller-runtime fake (for the
// VibedApp) and a client-go fake clientset (for pod list + GetLogs).
func logsService(t *testing.T, app *vibedv1.VibedApp, pods ...*corev1.Pod) *Service {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(app).Build()
	objs := make([]runtime.Object, len(pods))
	for i, p := range pods {
		objs[i] = p
	}
	return &Service{Client: c, Clientset: k8sfake.NewSimpleClientset(objs...), Namespace: "vibed-apps"}
}

func boundPod(name, podIP string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "vibed-apps",
			Labels: map[string]string{claimUIDLabel: "uid-" + name},
		},
		Status: corev1.PodStatus{PodIP: podIP},
	}
}

func appWithPodIP(name, owner, podIP string) *vibedv1.VibedApp {
	return &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "vibed-apps"},
		Spec:       vibedv1.VibedAppSpec{Owner: owner},
		Status:     vibedv1.VibedAppStatus{PodIP: podIP},
	}
}

func TestStreamLogsStreamsBoundPod(t *testing.T) {
	svc := logsService(t, appWithPodIP("app1", "alice", "10.0.0.5"), boundPod("app1-pod", "10.0.0.5"))

	var lines []string
	err := svc.StreamLogs(context.Background(), "alice", "app1", false, 0, func(l string) error {
		lines = append(lines, l)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}
	if len(lines) == 0 {
		t.Fatal("expected at least one log line from the fake stream")
	}
}

func TestStreamLogsOwnership(t *testing.T) {
	svc := logsService(t, appWithPodIP("app1", "bob", "10.0.0.5"), boundPod("app1-pod", "10.0.0.5"))

	err := svc.StreamLogs(context.Background(), "alice", "app1", false, 0, func(string) error { return nil })
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner StreamLogs = %v, want ErrNotFound", err)
	}
}

func TestStreamLogsNoPod(t *testing.T) {
	// App exists and is owned, but has no bound pod IP yet.
	svc := logsService(t, appWithPodIP("app1", "alice", ""))

	err := svc.StreamLogs(context.Background(), "alice", "app1", false, 0, func(string) error { return nil })
	if !errors.Is(err, ErrNoPod) {
		t.Fatalf("StreamLogs with no pod = %v, want ErrNoPod", err)
	}
}
