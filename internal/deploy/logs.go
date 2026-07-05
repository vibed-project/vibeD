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

// claimUIDLabel is the label agent-sandbox stamps on a bound Sandbox pod (its
// SandboxClaim's UID). Only bound pods carry it, so selecting on its presence
// narrows the pod list to live app pods.
const claimUIDLabel = "agents.x-k8s.io/claim-uid"

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

// boundPodName resolves the app's currently bound Sandbox pod by its
// status.podIP. The lookup is pushed to the API server via a field selector so
// we fetch only the matching pod, not every claim-labeled pod in the namespace
// (#77 — that scan grew with cluster size on every log request). The claim-uid
// label selector still restricts the result to bound app pods, and we re-check
// the IP in-process as a safety net.
func (s *Service) boundPodName(ctx context.Context, app *vibedv1.VibedApp) (string, error) {
	if app.Status.PodIP == "" {
		return "", ErrNoPod
	}
	pods, err := s.Clientset.CoreV1().Pods(app.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: claimUIDLabel,
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
