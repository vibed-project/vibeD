package mcp

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deploy"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/internal/tarball"
	"github.com/vibed-project/vibeD/pkg/api"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The tests drive each tool end-to-end through an in-memory MCP client/server
// session, so both the deploy.Service routing and the exact wire-level JSON
// shape are exercised.

// newSession registers the vibeD tools on a fresh MCP server and returns a
// connected in-memory client session.
func newSession(t *testing.T, deploySvc *deploy.Service) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "vibed-test", Version: "0.0.0"}, nil)
	RegisterTools(server, deploySvc, config.LimitsConfig{}, nil)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })

	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	cs, err := c.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callTool invokes a tool and returns its structured JSON output as a map.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if res.IsError {
		t.Fatalf("CallTool(%s) returned tool error: %s", name, resultText(res))
	}
	out, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("CallTool(%s): structuredContent is %T, want object", name, res.StructuredContent)
	}
	return out
}

// callToolExpectError invokes a tool and requires a tool-level error result,
// returning its text.
func callToolExpectError(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("CallTool(%s) succeeded, want tool error", name)
	}
	return resultText(res)
}

func resultText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// assertKeys fails unless m has exactly the given JSON keys — the MCP output
// shape is a client contract, so a missing or extra key is a regression.
func assertKeys(t *testing.T, m map[string]any, want ...string) {
	t.Helper()
	got := make([]string, 0, len(m))
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	w := append([]string(nil), want...)
	sort.Strings(w)
	if !reflect.DeepEqual(got, w) {
		t.Fatalf("JSON keys = %v, want %v", got, w)
	}
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := vibedv1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return s
}

// liveApp builds a VibedApp CR with an optional deploy history stashed in the
// deploy service's versions annotation.
func liveApp(name, owner, url string, phase vibedv1.Phase, recs []deploy.VersionRecord) *vibedv1.VibedApp {
	app := &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "vibed-apps"},
		Spec:       vibedv1.VibedAppSpec{Owner: owner},
		Status:     vibedv1.VibedAppStatus{Phase: phase, URL: url},
	}
	if len(recs) > 0 {
		b, _ := json.Marshal(recs)
		app.Annotations = map[string]string{"vibed.dev/versions": string(b)}
	}
	return app
}

// liveService builds a deploy.Service over a controller-runtime fake seeded
// with apps, mirroring internal/deploy's own tests.
func liveService(t *testing.T, apps ...*vibedv1.VibedApp) *deploy.Service {
	t.Helper()
	objs := make([]client.Object, len(apps))
	for i, a := range apps {
		objs[i] = a
	}
	c := ctrlfake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithStatusSubresource(&vibedv1.VibedApp{}).
		WithObjects(objs...).
		Build()
	return &deploy.Service{
		Client:        c,
		Store:         &fakeTarballStore{},
		Namespace:     "vibed-apps",
		DeployTimeout: time.Second,
		PollInterval:  10 * time.Millisecond,
	}
}

// fakeTarballStore is an in-memory tarball.Store; the tools under test only
// delete evicted version blobs, so no bytes need to be retained.
type fakeTarballStore struct{}

func (f *fakeTarballStore) Put(_ context.Context, id string, r io.Reader) (string, error) {
	_, _ = io.Copy(io.Discard, r)
	return "http://store.test/" + id + ".tar.gz", nil
}

func (f *fakeTarballStore) Delete(context.Context, string) error { return nil }

var _ tarball.Store = (*fakeTarballStore)(nil)

// fakeShareLinkStore is an in-memory store.ShareLinkStore.
type fakeShareLinkStore struct {
	mu     sync.Mutex
	links  []*api.ShareLink
	hashes map[string]string
}

func newFakeShareLinkStore() *fakeShareLinkStore {
	return &fakeShareLinkStore{hashes: map[string]string{}}
}

func (f *fakeShareLinkStore) CreateShareLink(_ context.Context, link *api.ShareLink, passwordHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *link
	f.links = append(f.links, &cp)
	f.hashes[link.Token] = passwordHash
	return nil
}

func (f *fakeShareLinkStore) GetShareLink(_ context.Context, token string) (*api.ShareLink, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.links {
		if l.Token == token {
			cp := *l
			return &cp, f.hashes[token], nil
		}
	}
	return nil, "", &api.ErrShareLinkNotFound{Token: token}
}

func (f *fakeShareLinkStore) ListShareLinks(_ context.Context, artifactID string, _, _ int) ([]api.ShareLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []api.ShareLink
	for _, l := range f.links {
		if l.ArtifactID == artifactID {
			out = append(out, *l)
		}
	}
	return out, nil
}

func (f *fakeShareLinkStore) RevokeShareLink(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.links {
		if l.Token == token {
			l.Revoked = true
			return nil
		}
	}
	return &api.ErrShareLinkNotFound{Token: token}
}

var _ store.ShareLinkStore = (*fakeShareLinkStore)(nil)

// TestToolsWithoutDeployService verifies that when the deploy service is nil
// (its prerequisites weren't configured), the artifact-lifecycle tools return a
// clear "not configured" error instead of panicking.
func TestToolsWithoutDeployService(t *testing.T) {
	cs := newSession(t, nil)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"deploy_artifact", map[string]any{"name": "x", "files": map[string]any{"index.html": "hi"}}},
		{"list_artifacts", map[string]any{}},
		{"get_artifact_status", map[string]any{"artifact_id": "x"}},
		{"delete_artifact", map[string]any{"artifact_id": "x"}},
		{"list_versions", map[string]any{"artifact_id": "x"}},
		{"create_share_link", map[string]any{"artifact_id": "x"}},
	} {
		msg := callToolExpectError(t, cs, tc.tool, tc.args)
		if !strings.Contains(msg, "deploy service not configured") {
			t.Errorf("%s error = %q, want 'deploy service not configured'", tc.tool, msg)
		}
	}
}
