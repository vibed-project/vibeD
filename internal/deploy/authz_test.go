package deploy

import (
	"context"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vibed-project/vibeD/internal/authz"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// stubAuthorizer is a minimal authz.Authorizer for exercising the deploy
// service's authorization wiring: owners control their own existing apps;
// listed "readers" may read any app; listed "writers" may write any app.
type stubAuthorizer struct {
	readers map[string]bool
	writers map[string]bool
}

func (s stubAuthorizer) Authorize(_ context.Context, req authz.Request) error {
	// Owner controls their own existing resource (deploy excluded — creation).
	if req.Action != authz.ActionAppDeploy && req.Resource.Owner != "" && req.Resource.Owner == req.Subject {
		return nil
	}
	switch req.Action {
	case authz.ActionAppGet, authz.ActionAppList:
		if s.readers[req.Subject] {
			return nil
		}
	default:
		if s.writers[req.Subject] {
			return nil
		}
	}
	return &authz.DeniedError{Action: req.Action, Reason: "stub deny"}
}

func appNames(apps []vibedv1.VibedApp) []string {
	out := make([]string, len(apps))
	for i, a := range apps {
		out[i] = a.Name
	}
	return out
}

// TestListScopingWithAuthorizer covers #21: an Authorizer expands List to the
// apps the caller may read (team-scoped visibility), replacing owner-only.
func TestListScopingWithAuthorizer(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).
		WithObjects(
			&vibedv1.VibedApp{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "vibed-apps"}, Spec: vibedv1.VibedAppSpec{Owner: "alice"}},
			&vibedv1.VibedApp{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "vibed-apps"}, Spec: vibedv1.VibedAppSpec{Owner: "bob"}},
		).Build()
	svc := newService(c, newFakeStore())
	svc.Authz = stubAuthorizer{readers: map[string]bool{"carol": true}}

	// carol is a team reader (a Viewer): sees both apps read-only.
	if list, total, err := svc.List(context.Background(), "carol", 0, 0); err != nil || len(list) != 2 || total != 2 {
		t.Fatalf("carol List = %v total=%d (err %v), want 2", appNames(list), total, err)
	}
	// dave has no read grant and owns nothing: sees none.
	if list, total, err := svc.List(context.Background(), "dave", 0, 0); err != nil || len(list) != 0 || total != 0 {
		t.Fatalf("dave List = %v total=%d (err %v), want 0", appNames(list), total, err)
	}
	// alice still sees her own app via ownership.
	list, total, err := svc.List(context.Background(), "alice", 0, 0)
	if err != nil || len(list) != 1 || list[0].Name != "a" || total != 1 {
		t.Fatalf("alice List = %v total=%d (err %v), want [a]", appNames(list), total, err)
	}
}

// TestWriteAuthorizationWithAuthorizer covers the read/write split: a teammate
// who may read (Get succeeds) is still denied a write action (Delete → 403).
func TestWriteAuthorizationWithAuthorizer(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).
		WithObjects(
			&vibedv1.VibedApp{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "vibed-apps"}, Spec: vibedv1.VibedAppSpec{Owner: "bob"}},
		).Build()
	svc := newService(c, newFakeStore())
	svc.Authz = stubAuthorizer{readers: map[string]bool{"carol": true}}

	// carol may read bob's app...
	if _, err := svc.Get(context.Background(), "carol", "b"); err != nil {
		t.Fatalf("carol Get b: want allow, got %v", err)
	}
	// ...but not delete it: a Forbidden authz denial.
	if err := svc.Delete(context.Background(), "carol", "b"); !authz.IsDenied(err) {
		t.Fatalf("carol Delete b: want denied, got %v", err)
	}
	// The app is still there.
	if _, err := svc.Get(context.Background(), "bob", "b"); err != nil {
		t.Fatalf("b should still exist: %v", err)
	}
}

// --- optional list interfaces (authz.ListScoper / authz.BatchAuthorizer) ----

// deptAuthorizer grants reads by department membership: a subject may read
// apps whose department is in its set, plus apps it owns. decide holds the
// single decision function so the batch variant below provably decides
// without going through Authorize; perItem counts Authorize invocations to
// observe which path (and how many candidates) List exercised.
type deptAuthorizer struct {
	depts   map[string]map[string]bool // subject -> readable departments
	perItem *int                       // Authorize call count (optional)
}

func (d deptAuthorizer) decide(req authz.Request) error {
	if req.Resource.Owner != "" && req.Resource.Owner == req.Subject {
		return nil
	}
	if d.depts[req.Subject][req.Resource.Department] {
		return nil
	}
	return &authz.DeniedError{Action: req.Action, Reason: "dept deny"}
}

func (d deptAuthorizer) Authorize(_ context.Context, req authz.Request) error {
	if d.perItem != nil {
		*d.perItem++
	}
	return d.decide(req)
}

// scopingDeptAuthorizer adds ListScoper: it returns a fixed selector (nil =
// decline narrowing) and captures the collection Request it was handed.
type scopingDeptAuthorizer struct {
	deptAuthorizer
	sel    labels.Selector
	gotReq *authz.Request
}

func (s scopingDeptAuthorizer) ScopeList(_ context.Context, req authz.Request) (labels.Selector, error) {
	if s.gotReq != nil {
		*s.gotReq = req
	}
	return s.sel, nil
}

// batchingDeptAuthorizer adds BatchAuthorizer over the same decide function;
// mismatch makes it violate the length contract to exercise the fallback.
type batchingDeptAuthorizer struct {
	deptAuthorizer
	batch    *int // AuthorizeBatch call count (optional)
	mismatch bool
}

func (b batchingDeptAuthorizer) AuthorizeBatch(_ context.Context, reqs []authz.Request) []error {
	if b.batch != nil {
		*b.batch++
	}
	if b.mismatch {
		return make([]error, len(reqs)/2) // wrong length: reply must be discarded
	}
	errs := make([]error, len(reqs))
	for i := range reqs {
		errs[i] = b.decide(reqs[i])
	}
	return errs
}

// fullDeptAuthorizer implements both optional interfaces.
type fullDeptAuthorizer struct {
	batchingDeptAuthorizer
	sel labels.Selector
}

func (f fullDeptAuthorizer) ScopeList(context.Context, authz.Request) (labels.Selector, error) {
	return f.sel, nil
}

// deptFixture: three apps in the tenant namespace, two in department blue,
// one in red. carolDepts grants carol read on blue only, so her visible set
// is exactly [a b].
func deptFixture(t *testing.T) client.Client {
	t.Helper()
	dept := func(d string) map[string]string { return map[string]string{vibedv1.LabelDepartment: d} }
	return fake.NewClientBuilder().WithScheme(newScheme(t)).WithStatusSubresource(&vibedv1.VibedApp{}).
		WithObjects(
			&vibedv1.VibedApp{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "vibed-apps", Labels: dept("blue")}, Spec: vibedv1.VibedAppSpec{Owner: "alice"}},
			&vibedv1.VibedApp{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "vibed-apps", Labels: dept("blue")}, Spec: vibedv1.VibedAppSpec{Owner: "bob"}},
			&vibedv1.VibedApp{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "vibed-apps", Labels: dept("red")}, Spec: vibedv1.VibedAppSpec{Owner: "cara"}},
		).Build()
}

func carolDepts() map[string]map[string]bool {
	return map[string]map[string]bool{"carol": {"blue": true}}
}

func mustParseSelector(t *testing.T, s string) labels.Selector {
	t.Helper()
	sel, err := labels.Parse(s)
	if err != nil {
		t.Fatalf("parse selector %q: %v", s, err)
	}
	return sel
}

// TestListOptionalInterfacesEquivalence: the same data and subject yield
// identical List results whether the Authorizer implements neither optional
// interface, ListScoper only, BatchAuthorizer only, or both. The scoper's
// selector deliberately OVER-returns (it also matches the red app c, which
// carol may not read) to prove candidates are still exactly filtered.
func TestListOptionalInterfacesEquivalence(t *testing.T) {
	over := mustParseSelector(t, vibedv1.LabelDepartment+" in (blue,red)")
	base := deptAuthorizer{depts: carolDepts()}
	variants := map[string]authz.Authorizer{
		"plain":   base,
		"scoper":  scopingDeptAuthorizer{deptAuthorizer: base, sel: over},
		"batcher": batchingDeptAuthorizer{deptAuthorizer: base},
		"both":    fullDeptAuthorizer{batchingDeptAuthorizer{deptAuthorizer: base}, over},
	}
	for name, a := range variants {
		t.Run(name, func(t *testing.T) {
			svc := newService(deptFixture(t), newFakeStore())
			svc.Authz = a
			list, total, err := svc.List(context.Background(), "carol", 0, 0)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if got := appNames(list); !reflect.DeepEqual(got, []string{"a", "b"}) || total != 2 {
				t.Fatalf("List = %v total=%d, want [a b] total=2", got, total)
			}
		})
	}
}

// TestListScoperNarrowsFetch: a narrowing selector shrinks the candidate set
// reaching Authorize (2 calls instead of 3) without changing the result; a
// nil selector narrows nothing (all 3 apps fetched and checked). Also pins
// the collection Request handed to ScopeList.
func TestListScoperNarrowsFetch(t *testing.T) {
	perItem := 0
	var gotReq authz.Request
	svc := newService(deptFixture(t), newFakeStore())
	svc.Authz = scopingDeptAuthorizer{
		deptAuthorizer: deptAuthorizer{depts: carolDepts(), perItem: &perItem},
		sel:            mustParseSelector(t, vibedv1.LabelDepartment+"=blue"),
		gotReq:         &gotReq,
	}
	list, total, err := svc.List(context.Background(), "carol", 0, 0)
	if err != nil || total != 2 || !reflect.DeepEqual(appNames(list), []string{"a", "b"}) {
		t.Fatalf("List = %v total=%d (err %v), want [a b] total=2", appNames(list), total, err)
	}
	if perItem != 2 {
		t.Fatalf("Authorize calls = %d, want 2 (selector should exclude the red app)", perItem)
	}
	if gotReq.Action != authz.ActionAppList || gotReq.Subject != "carol" ||
		gotReq.Resource.Kind != "app" || gotReq.Resource.ID != "" || gotReq.Resource.Namespace != "vibed-apps" {
		t.Fatalf("ScopeList request = %+v, want app.list collection request in vibed-apps", gotReq)
	}

	// nil selector = decline narrowing: the whole namespace is fetched.
	perItem = 0
	svc.Authz = scopingDeptAuthorizer{deptAuthorizer: deptAuthorizer{depts: carolDepts(), perItem: &perItem}}
	list, total, err = svc.List(context.Background(), "carol", 0, 0)
	if err != nil || total != 2 || !reflect.DeepEqual(appNames(list), []string{"a", "b"}) {
		t.Fatalf("List = %v total=%d (err %v), want [a b] total=2", appNames(list), total, err)
	}
	if perItem != 3 {
		t.Fatalf("Authorize calls = %d, want 3 (nil selector must not narrow)", perItem)
	}
}

// TestListBatchAuthorizerFallback: a well-formed batch reply replaces the
// per-item loop entirely; a length-mismatched reply is discarded and List
// falls back to per-item Authorize with identical results.
func TestListBatchAuthorizerFallback(t *testing.T) {
	perItem, batch := 0, 0
	svc := newService(deptFixture(t), newFakeStore())
	svc.Authz = batchingDeptAuthorizer{
		deptAuthorizer: deptAuthorizer{depts: carolDepts(), perItem: &perItem},
		batch:          &batch,
	}
	list, total, err := svc.List(context.Background(), "carol", 0, 0)
	if err != nil || total != 2 || !reflect.DeepEqual(appNames(list), []string{"a", "b"}) {
		t.Fatalf("List = %v total=%d (err %v), want [a b] total=2", appNames(list), total, err)
	}
	if batch != 1 || perItem != 0 {
		t.Fatalf("batch=%d perItem=%d, want one batch call and no per-item calls", batch, perItem)
	}

	// Mismatched reply: fall back to per-item, same results.
	perItem, batch = 0, 0
	svc.Authz = batchingDeptAuthorizer{
		deptAuthorizer: deptAuthorizer{depts: carolDepts(), perItem: &perItem},
		batch:          &batch,
		mismatch:       true,
	}
	list, total, err = svc.List(context.Background(), "carol", 0, 0)
	if err != nil || total != 2 || !reflect.DeepEqual(appNames(list), []string{"a", "b"}) {
		t.Fatalf("List = %v total=%d (err %v), want [a b] total=2", appNames(list), total, err)
	}
	if batch != 1 || perItem != 3 {
		t.Fatalf("batch=%d perItem=%d, want the mismatched batch discarded and 3 per-item calls", batch, perItem)
	}
}
