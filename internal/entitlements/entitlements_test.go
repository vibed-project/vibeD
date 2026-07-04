package entitlements

import "testing"

type fakeEnt struct {
	edition string
	enabled map[string]bool
}

func (f fakeEnt) Edition() string       { return f.edition }
func (f fakeEnt) Enabled(k string) bool { return f.enabled[k] }

func TestDefaultCommunityEnablesNothing(t *testing.T) {
	Set(nil) // reset to community
	if Get().Edition() != "community" {
		t.Fatalf("default edition = %q, want community", Get().Edition())
	}
	if err := Require("postgres"); err == nil {
		t.Error("community edition must not enable gated features")
	}
}

func TestSetAndRequire(t *testing.T) {
	Set(fakeEnt{edition: "pro", enabled: map[string]bool{"saml": true}})
	t.Cleanup(func() { Set(nil) })

	if err := Require("saml"); err != nil {
		t.Errorf("enabled feature should pass: %v", err)
	}
	if err := Require("postgres"); err == nil {
		t.Error("a feature that is not enabled should fail")
	}
	if Get().Edition() != "pro" {
		t.Errorf("edition = %q, want pro", Get().Edition())
	}
}
