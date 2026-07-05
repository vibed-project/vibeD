package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// appInPhaseFor builds an app whose Ready condition transitioned `ago` in the
// past, so transitionalRequeue sees that much time-in-phase.
func appInPhaseFor(ago time.Duration) *vibedv1.VibedApp {
	return &vibedv1.VibedApp{
		Status: vibedv1.VibedAppStatus{
			Phase: vibedv1.PhaseStarting,
			Conditions: []metav1.Condition{{
				Type:               ConditionReady,
				Status:             metav1.ConditionFalse,
				Reason:             ReasonStarting,
				LastTransitionTime: metav1.NewTime(time.Now().Add(-ago)),
			}},
		},
	}
}

// TestApplyDefaults confirms the new perf-related defaults (#68, #70).
func TestApplyDefaults(t *testing.T) {
	r := &Reconciler{}
	r.applyDefaults()
	if r.RequeueDelay != 2*time.Second {
		t.Errorf("RequeueDelay default = %v, want 2s", r.RequeueDelay)
	}
	if r.MaxRequeueDelay != 60*time.Second {
		t.Errorf("MaxRequeueDelay default = %v, want 60s", r.MaxRequeueDelay)
	}
	if r.MaxConcurrentReconciles != 4 {
		t.Errorf("MaxConcurrentReconciles default = %d, want 4", r.MaxConcurrentReconciles)
	}
}

// TestTransitionalRequeueBackoff is the proof for #70: the requeue delay grows
// (capped) the longer an app is stuck in a transitional phase, instead of a
// fixed 2s poll.
func TestTransitionalRequeueBackoff(t *testing.T) {
	r := &Reconciler{RequeueDelay: 2 * time.Second, MaxRequeueDelay: 60 * time.Second}

	// Freshly transitioned (no meaningful time in phase) → base delay.
	fresh := r.transitionalRequeue(appInPhaseFor(0))
	if fresh != 2*time.Second {
		t.Errorf("fresh app requeue = %v, want base 2s", fresh)
	}

	// A little while in phase → larger than base.
	mid := r.transitionalRequeue(appInPhaseFor(10 * time.Second))
	if mid <= 2*time.Second {
		t.Errorf("app stuck 10s should back off beyond base, got %v", mid)
	}
	if mid > 60*time.Second {
		t.Errorf("backoff must be capped at MaxRequeueDelay, got %v", mid)
	}

	// Stuck a long time → capped at max.
	old := r.transitionalRequeue(appInPhaseFor(1 * time.Hour))
	if old != 60*time.Second {
		t.Errorf("long-stuck app requeue = %v, want capped 60s", old)
	}

	// Backoff is monotonic non-decreasing with time in phase.
	if r.transitionalRequeue(appInPhaseFor(30*time.Second)) < mid {
		t.Error("backoff should not decrease as time-in-phase grows")
	}
}

// TestTransitionalRequeueNoCondition: an app with no Ready condition yet falls
// back to the base delay.
func TestTransitionalRequeueNoCondition(t *testing.T) {
	r := &Reconciler{RequeueDelay: 2 * time.Second, MaxRequeueDelay: 60 * time.Second}
	got := r.transitionalRequeue(&vibedv1.VibedApp{})
	if got != 2*time.Second {
		t.Errorf("app with no Ready condition requeue = %v, want base 2s", got)
	}
}
