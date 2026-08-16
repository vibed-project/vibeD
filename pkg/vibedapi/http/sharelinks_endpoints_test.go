package vibedhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/classifier"
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deploy"
	"github.com/vibed-project/vibeD/internal/tarball"
	"github.com/vibed-project/vibeD/pkg/api"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// fakeShareStore is an in-memory store.ShareLinkStore for the handler tests.
// It preserves insertion order so pagination assertions are deterministic.
type fakeShareStore struct {
	links []*api.ShareLink
	hash  map[string]string
}

func newFakeShareStore() *fakeShareStore {
	return &fakeShareStore{hash: map[string]string{}}
}

func (f *fakeShareStore) CreateShareLink(_ context.Context, l *api.ShareLink, h string) error {
	cp := *l
	f.links = append(f.links, &cp)
	f.hash[l.Token] = h
	return nil
}

func (f *fakeShareStore) GetShareLink(_ context.Context, t string) (*api.ShareLink, string, error) {
	for _, l := range f.links {
		if l.Token == t {
			return l, f.hash[t], nil
		}
	}
	return nil, "", &api.ErrShareLinkNotFound{Token: t}
}

func (f *fakeShareStore) ListShareLinks(_ context.Context, artifactID string, limit, offset int) ([]api.ShareLink, error) {
	var out []api.ShareLink
	for _, l := range f.links {
		if l.ArtifactID == artifactID {
			out = append(out, *l)
		}
	}
	if offset > 0 {
		if offset < len(out) {
			out = out[offset:]
		} else {
			out = nil
		}
	}
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeShareStore) RevokeShareLink(_ context.Context, t string) error {
	for _, l := range f.links {
		if l.Token == t {
			l.Revoked = true
		}
	}
	return nil
}

// newShareServer builds a Server backed by a real deploy.Service with an
// in-memory share-link store. Callers wrap it with shareHandler to inject an
// authenticated owner the way the auth middleware would.
func newShareServer(t *testing.T) (*Server, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := vibedv1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&vibedv1.VibedApp{}).Build()

	store, err := tarball.New(config.TarballConfig{
		Backend: "served",
		Served:  config.ServedTarballConfig{BasePath: t.TempDir(), PublicBaseURL: "http://vibed.test"},
	})
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	srv := New(nil, nil, nil, nil)
	srv.Deploy = &deploy.Service{
		Client:        c,
		Store:         store,
		Classifier:    classifier.Classifier{},
		Namespace:     "vibed-apps",
		DeployTimeout: time.Second,
		PollInterval:  10 * time.Millisecond,
		ShareLinks:    newFakeShareStore(),
		BaseURL:       "https://apps.example.test",
	}
	return srv, c
}

// shareHandler wires srv onto a fresh mux and injects owner into the request
// context, mirroring how main.go stacks the auth middleware in front of the API.
func shareHandler(srv *Server, owner string) http.Handler {
	mux := http.NewServeMux()
	HandlerFromMux(srv, mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(vibedauth.WithUserID(r.Context(), owner)))
	})
}

// seedApp creates a VibedApp owned by owner so the ownership-checked share-link
// endpoints resolve it.
func seedApp(t *testing.T, c client.Client, name, owner string) {
	t.Helper()
	app := &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "vibed-apps",
			Labels:    map[string]string{vibedv1.LabelOwner: vibedv1.SanitizeLabel(owner)},
		},
		Spec: vibedv1.VibedAppSpec{Owner: owner},
	}
	if err := c.Create(context.Background(), app); err != nil {
		t.Fatalf("seed app: %v", err)
	}
}

func createLink(t *testing.T, h http.Handler, appID, bodyJSON string) *httptest.ResponseRecorder {
	t.Helper()
	var body *bytes.Buffer
	if bodyJSON == "" {
		body = &bytes.Buffer{}
	} else {
		body = bytes.NewBufferString(bodyJSON)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/apps/"+appID+"/share-links", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCreateShareLinkEndpoint(t *testing.T) {
	srv, c := newShareServer(t)
	h := shareHandler(srv, "alice@example.com")
	seedApp(t, c, "myapp", "alice@example.com")

	// Password-protected, time-limited link -> 200 + a populated ShareLink.
	rec := createLink(t, h, "myapp", `{"password":"hunter2","expires_in":"24h"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var link ShareLink
	if err := json.Unmarshal(rec.Body.Bytes(), &link); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if link.Token == "" || link.ArtifactId != "myapp" || !link.HasPassword {
		t.Fatalf("unexpected link: %+v", link)
	}
	if link.ExpiresAt == nil {
		t.Errorf("expires_in was set but expires_at is nil")
	}
	if link.Url == nil || *link.Url == "" {
		t.Errorf("url should be populated from BaseURL, got %+v", link.Url)
	}

	// Empty body mints a passwordless, never-expiring link. Decode into a fresh
	// value: an omitted expires_at leaves a reused struct's stale pointer intact.
	rec = createLink(t, h, "myapp", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("create (empty body) = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var bare ShareLink
	if err := json.Unmarshal(rec.Body.Bytes(), &bare); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if bare.HasPassword {
		t.Errorf("empty body should mint a passwordless link")
	}
	if bare.ExpiresAt != nil {
		t.Errorf("empty body should mint a never-expiring link")
	}
}

// TestCreateShareLinkAcceptsDayShorthand locks in the "7d" day shorthand the
// dashboard emits, which time.ParseDuration alone does not understand.
func TestCreateShareLinkAcceptsDayShorthand(t *testing.T) {
	srv, c := newShareServer(t)
	h := shareHandler(srv, "alice@example.com")
	seedApp(t, c, "myapp", "alice@example.com")

	rec := createLink(t, h, "myapp", `{"expires_in":"7d"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create with 7d = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var link ShareLink
	_ = json.Unmarshal(rec.Body.Bytes(), &link)
	if link.ExpiresAt == nil {
		t.Fatal("7d expires_in should produce an expires_at")
	}
	// ~7 days out (allow slack for test execution time).
	if d := time.Until(*link.ExpiresAt); d < 6*24*time.Hour || d > 8*24*time.Hour {
		t.Errorf("expiry %v not ~7 days out", d)
	}
}

func TestCreateShareLinkRejectsBadExpiry(t *testing.T) {
	srv, c := newShareServer(t)
	h := shareHandler(srv, "alice@example.com")
	seedApp(t, c, "myapp", "alice@example.com")

	// A non-empty but unparseable expires_in must be a 400, never a silent
	// permanent link (fail-open on a security control).
	rec := createLink(t, h, "myapp", `{"expires_in":"soon"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad expires_in = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	var e Error
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if e.Code != "bad_expires_in" {
		t.Errorf("code = %q, want bad_expires_in", e.Code)
	}
}

// TestShareLinkOwnerScoping ensures a caller cannot create or list links for an
// app they do not own — it 404s (not 403) to avoid leaking the app's existence.
func TestShareLinkOwnerScoping(t *testing.T) {
	srv, c := newShareServer(t)
	seedApp(t, c, "alices-app", "alice@example.com")

	bob := shareHandler(srv, "bob@example.com")

	rec := createLink(t, bob, "alices-app", `{"password":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bob create on alice's app = %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	bob.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/apps/alices-app/share-links", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bob list on alice's app = %d, want 404", rec.Code)
	}
}

func TestListShareLinksPagination(t *testing.T) {
	srv, c := newShareServer(t)
	h := shareHandler(srv, "alice@example.com")
	seedApp(t, c, "myapp", "alice@example.com")

	// Mint three links.
	for i := 0; i < 3; i++ {
		if rec := createLink(t, h, "myapp", ""); rec.Code != http.StatusOK {
			t.Fatalf("seed link %d = %d; body=%s", i, rec.Code, rec.Body.String())
		}
	}

	list := func(query string) (items []ShareLink, total int) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/apps/myapp/share-links"+query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("list%s = %d; body=%s", query, rec.Code, rec.Body.String())
		}
		var body struct {
			Items []ShareLink `json:"items"`
			Total int         `json:"total"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode list%s: %v", query, err)
		}
		return body.Items, body.Total
	}

	// Default: everything, total intact.
	all, total := list("")
	if len(all) != 3 || total != 3 {
		t.Fatalf("default list = %d items total=%d, want 3/3", len(all), total)
	}

	// Pages report the full total; concatenation preserves ordering.
	p1, t1 := list("?limit=2")
	p2, t2 := list("?limit=2&offset=2")
	if len(p1) != 2 || len(p2) != 1 || t1 != 3 || t2 != 3 {
		t.Fatalf("pages = %d/%d totals = %d/%d, want 2/1 and 3/3", len(p1), len(p2), t1, t2)
	}
	if p1[0].Token != all[0].Token || p2[0].Token != all[2].Token {
		t.Errorf("paged ordering != default ordering")
	}

	// Offset past the end: empty page, total intact.
	if items, tot := list("?limit=2&offset=99"); len(items) != 0 || tot != 3 {
		t.Fatalf("past-end page = %d items total=%d, want 0/3", len(items), tot)
	}

	// Negative offset clamps to 0.
	if items, _ := list("?limit=2&offset=-5"); len(items) != 2 || items[0].Token != p1[0].Token {
		t.Fatalf("negative offset page = %+v, want page 1", items)
	}
}

// TestShareLinkEndpointsNotImplementedWhenDeployNil locks in that share-link
// endpoints degrade to 501 (not a panic) when the deploy service is absent,
// matching the other deploy-backed endpoints so the server still boots in
// ops-only mode.
func TestShareLinkEndpointsNotImplementedWhenDeployNil(t *testing.T) {
	srv := New(nil, nil, nil, nil) // no Deploy service
	mux := http.NewServeMux()
	HandlerFromMux(srv, mux)

	cases := []struct{ method, path string }{
		{http.MethodPost, "/v1/apps/some-app/share-links"},
		{http.MethodGet, "/v1/apps/some-app/share-links"},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != http.StatusNotImplemented {
				t.Fatalf("status = %d, want 501; body=%s", rec.Code, rec.Body.String())
			}
			var e Error
			if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
				t.Fatalf("unmarshal Error: %v", err)
			}
			if e.Code == "" {
				t.Errorf("expected an error code, got empty")
			}
		})
	}
}

// TestListShareLinksEmptyIsNonNil ensures an app with no links returns an empty
// (non-null) items array so clients can iterate without a null check.
func TestListShareLinksEmptyIsNonNil(t *testing.T) {
	srv, c := newShareServer(t)
	h := shareHandler(srv, "alice@example.com")
	seedApp(t, c, "myapp", "alice@example.com")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/apps/myapp/share-links", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []ShareLink `json:"items"`
		Total int         `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Items == nil {
		t.Errorf("items must be a non-nil empty list, not null")
	}
	if body.Total != 0 {
		t.Errorf("total = %d, want 0", body.Total)
	}
}
