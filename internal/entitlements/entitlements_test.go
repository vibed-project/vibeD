package entitlements

import "testing"

type fakeEnt struct {
	edition  string
	licensed map[string]bool
}

func (f fakeEnt) Edition() string          { return f.edition }
func (f fakeEnt) Licensed(k string) bool   { return f.licensed[k] }

func TestDefaultCommunityLicensesNothing(t *testing.T) {
	Set(nil) // reset to community
	if Get().Edition() != "community" {
		t.Fatalf("default edition = %q, want community", Get().Edition())
	}
	if err := Require("postgres"); err == nil {
		t.Error("community edition must not license paid features")
	}
}

func TestSetAndRequire(t *testing.T) {
	Set(fakeEnt{edition: "enterprise", licensed: map[string]bool{"saml": true}})
	t.Cleanup(func() { Set(nil) })

	if err := Require("saml"); err != nil {
		t.Errorf("licensed feature should pass: %v", err)
	}
	if err := Require("postgres"); err == nil {
		t.Error("unlicensed feature should fail")
	}
	if Get().Edition() != "enterprise" {
		t.Errorf("edition = %q, want enterprise", Get().Edition())
	}
}
