package deploy

import (
	"bufio"
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// sandboxPodLabel is the label agent-sandbox stamps on every Sandbox pod (an
// FNV hash of the Sandbox name; v0.5+ replaced the older per-claim label).
// Selecting on its presence narrows the pod list to sandbox pods; the specific
// bound pod is then matched by IP.
const sandboxPodLabel = "agents.x-k8s.io/sandbox-name-hash"

// ErrNoPod means the app exists but has no running pod yet (still claiming, or
// suspended), so there's nothing to stream.
var ErrNoPod = errors.New("app has no running pod")

// StreamLogs streams the bound pod's container logs for an app the owner owns,
// invoking line for each log line. With follow, it blocks until ctx is
// cancelled or the pod terminates. tail (>0) seeds the stream with the last N
// lines. Returns ErrNotFound (not owned/missing) or ErrNoPod (no live pod).
func (s *Service) StreamLogs(ctx context.Context, owner, id string, follow bool, tail int64, line func(string) error) error {
	s.defaults()
	if s.Clientset == nil {
		return errors.New("log streaming unavailable: no kubernetes clientset configured")
	}
	app, err := s.Get(ctx, owner, id) // ownership-checked; ErrNotFound if not owned
	if err != nil {
		return err
	}
	podName, err := s.boundPodName(ctx, app)
	if err != nil {
		return err
	}

	opts := &corev1.PodLogOptions{Follow: follow}
	if tail > 0 {
		opts.TailLines = &tail
	}
	stream, err := s.Clientset.CoreV1().Pods(app.Namespace).GetLogs(podName, opts).Stream(ctx)
	if err != nil {
		return fmt.Errorf("stream pod logs: %w", err)
	}
	defer stream.Close()

	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // tolerate long log lines
	for sc.Scan() {
		if err := line(sc.Text()); err != nil {
			return err
		}
	}
	return sc.Err()
}

// boundPodName resolves the app's currently bound Sandbox pod by matching its
// status.podIP against the sandbox-labeled pods in the namespace.
func (s *Service) boundPodName(ctx context.Context, app *vibedv1.VibedApp) (string, error) {
	if app.Status.PodIP == "" {
		return "", ErrNoPod
	}
	// Narrow to the one bound pod server-side via a field selector on status.podIP
	// instead of listing every sandbox pod in the namespace and scanning by IP on
	// each log-stream open (#77). The exact-IP check below stays as a backstop:
	// clients that don't honor the field selector (e.g. the fake used in tests)
	// just return the wider set and we match in Go, so correctness never depends
	// on server-side field filtering.
	pods, err := s.Clientset.CoreV1().Pods(app.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: sandboxPodLabel,
		FieldSelector: "status.podIP=" + app.Status.PodIP,
	})
	if err != nil {
		return "", fmt.Errorf("list bound pods: %w", err)
	}
	for i := range pods.Items {
		if pods.Items[i].Status.PodIP == app.Status.PodIP {
			return pods.Items[i].Name, nil
		}
	}
	return "", ErrNoPod
}
