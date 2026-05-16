package controller

import (
	"context"
	"regexp"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

func newAppNS(ns, name string) *vibedv1.VibedApp {
	return &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
	}
}

// labelShape matches the Caddyfile regex from refactor.md §5.5:
// `^[a-z0-9]{12}$`. Both vibed-controller's label and vibed-router's
// route matcher must agree on this shape.
var labelShape = regexp.MustCompile(`^[a-z0-9]{12}$`)

func TestAppLabelIsStable(t *testing.T) {
	app := newAppNS("vibed-apps", "hello")
	got1 := AppLabel(app)
	got2 := AppLabel(app)
	if got1 != got2 {
		t.Errorf("label is not stable: %q vs %q", got1, got2)
	}
}

func TestAppLabelMatchesRefactorShape(t *testing.T) {
	for _, name := range []string{"hello", "another-app", "x", "very-long-app-name-here"} {
		got := AppLabel(newAppNS("default", name))
		if !labelShape.MatchString(got) {
			t.Errorf("AppLabel(%q) = %q does not match %s", name, got, labelShape)
		}
	}
}

func TestAppLabelIsNamespaceSensitive(t *testing.T) {
	// Two apps with the same name but different namespaces must get
	// different labels — they're different apps and must not collide on
	// Caddy routes.
	a := AppLabel(newAppNS("ns-a", "hello"))
	b := AppLabel(newAppNS("ns-b", "hello"))
	if a == b {
		t.Errorf("same name in different namespaces produced same label: %q", a)
	}
}

func TestDeterministicRouterURLShape(t *testing.T) {
	r := DeterministicRouter{Domain: "vibed.example.com"}
	app := newAppNS("vibed-apps", "hello")
	url, err := r.Publish(context.Background(), app, "ignored-sandbox-ref")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	want := "https://" + AppLabel(app) + ".vibed.example.com"
	if url != want {
		t.Errorf("URL = %q, want %q", url, want)
	}
}

func TestDeterministicRouterDefaultsDomain(t *testing.T) {
	r := DeterministicRouter{} // no Domain — must fall back to vibed.example.com
	app := newAppNS("vibed-apps", "hello")
	url, _ := r.Publish(context.Background(), app, "")
	if want := "https://" + AppLabel(app) + ".vibed.example.com"; url != want {
		t.Errorf("URL = %q, want default %q", url, want)
	}
}
