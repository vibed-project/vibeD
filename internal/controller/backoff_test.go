package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

func readyCond(status metav1.ConditionStatus, since time.Time) vibedv1.VibedApp {
	return vibedv1.VibedApp{
		Status: vibedv1.VibedAppStatus{
			Conditions: []metav1.Condition{{
				Type:               ConditionReady,
				Status:             status,
				LastTransitionTime: metav1.NewTime(since),
			}},
		},
	}
}

// TestTransitionalRequeueBackoff locks in issue #70: transitional requeues start
// near RequeueDelay and back off exponentially (capped) the longer an app has
// been non-Ready, instead of a flat hot-poll forever.
func TestTransitionalRequeueBackoff(t *testing.T) {
	r := &Reconciler{RequeueDelay: 2 * time.Second}
	now := time.Now()

	cases := []struct {
		name string
		app  vibedv1.VibedApp
		want time.Duration
	}{
		{"no Ready condition yet → base", vibedv1.VibedApp{}, 2 * time.Second},
		{"currently Ready → base", readyCond(metav1.ConditionTrue, now.Add(-time.Hour)), 2 * time.Second},
		{"just went not-Ready → base", readyCond(metav1.ConditionFalse, now), 2 * time.Second},
		{"not-Ready ~6s (3 periods) → 16s", readyCond(metav1.ConditionFalse, now.Add(-6 * time.Second)), 16 * time.Second},
		{"long-stuck → capped at max", readyCond(metav1.ConditionFalse, now.Add(-10 * time.Minute)), maxRequeueDelay},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := r.transitionalRequeueAfter(&c.app)
			if got != c.want {
				t.Errorf("transitionalRequeueAfter = %s, want %s", got, c.want)
			}
			if got > maxRequeueDelay {
				t.Errorf("backoff %s exceeded the cap %s", got, maxRequeueDelay)
			}
		})
	}
}
