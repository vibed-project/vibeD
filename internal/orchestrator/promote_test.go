package orchestrator

import (
	"errors"
	"testing"

	"github.com/vibed-project/vibeD/pkg/api"
)

func TestCanPromote(t *testing.T) {
	tests := []struct {
		name     string
		artifact *api.Artifact
		wantErr  bool
	}{
		{"running preview on a runner", &api.Artifact{Mode: api.ModePreview, Target: api.TargetRunner}, false},
		{"already a built artifact", &api.Artifact{Mode: api.ModeBuilt, Target: api.TargetKubernetes}, true},
		{"preview mode but non-runner target", &api.Artifact{Mode: api.ModePreview, Target: api.TargetKubernetes}, true},
		{"runner target but built mode", &api.Artifact{Mode: api.ModeBuilt, Target: api.TargetRunner}, true},
		{"empty mode and target", &api.Artifact{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := canPromote(tt.artifact)
			if tt.wantErr {
				if err == nil {
					t.Fatal("canPromote() = nil, want error")
				}
				var invErr *api.ErrInvalidInput
				if !errors.As(err, &invErr) {
					t.Errorf("canPromote() error type = %T, want *api.ErrInvalidInput", err)
				}
			} else if err != nil {
				t.Errorf("canPromote() = %v, want nil", err)
			}
		})
	}
}
