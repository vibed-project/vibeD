package deploy

// Tests for the streaming (nil policy gate) deploy path: hash/classification/
// stored-byte equivalence with the buffered path, size-cap parity, spool
// cleanup, and the policy gate's Source + SourceOpener contract.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vibed-project/vibeD/internal/policy"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// nonSeeker hides Seek from an underlying reader, forcing Deploy onto the
// spool-to-temp-file streaming branch.
type nonSeeker struct{ r io.Reader }

func (n nonSeeker) Read(p []byte) (int, error) { return n.r.Read(p) }

// capturePolicy allows every deploy and records the Input it saw, so tests
// can assert the gate's Source/SourceOpener contract (and force the buffered
// path by merely being non-nil).
type capturePolicy struct {
	mu sync.Mutex
	in policy.Input
}

func (p *capturePolicy) Evaluate(_ context.Context, in policy.Input) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.in = in
	return nil
}

func (p *capturePolicy) got() policy.Input {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.in
}

// zeroReaderAt yields zeros forever; io.NewSectionReader over it gives an
// arbitrarily large seekable source without allocating the payload.
type zeroReaderAt struct{}

func (zeroReaderAt) ReadAt(p []byte, _ int64) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// The same tarball deployed through (i) the buffered path (non-nil policy
// gate), (ii) the streaming seekable path, and (iii) the streaming
// non-seekable (spool) path must record the identical sha256 in the version
// record, classify identically, and store byte-identical blobs.
func TestDeployStreamingAndBufferedPathsAgree(t *testing.T) {
	tb := gzTarball(t, map[string]string{"requirements.txt": "flask", "app.py": "print('hi')"})
	wantHash := fmt.Sprintf("%x", sha256.Sum256(tb))

	cases := []struct {
		name    string
		gate    policy.Gate
		tarball io.Reader
	}{
		{"buffered via policy gate", &capturePolicy{}, bytes.NewReader(tb)},
		{"streaming seekable", nil, bytes.NewReader(tb)},
		{"streaming non-seekable", nil, nonSeeker{bytes.NewReader(tb)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
			st := newFakeStore()
			svc := newService(c, st)
			svc.DeployTimeout = 50 * time.Millisecond // pending is fine; versions record pre-wait
			svc.Policy = tc.gate

			if _, err := svc.Deploy(context.Background(), Request{Name: "same", Owner: "alice", Tarball: tc.tarball}); err != nil {
				t.Fatalf("Deploy: %v", err)
			}

			versions, err := svc.Versions(context.Background(), "alice", "same")
			if err != nil || len(versions) != 1 {
				t.Fatalf("Versions: %v (n=%d)", err, len(versions))
			}
			v := versions[0]
			if v.SourceHash != wantHash {
				t.Errorf("SourceHash = %q, want %q", v.SourceHash, wantHash)
			}
			if v.Template != "python-313" {
				t.Errorf("classified template = %q, want python-313", v.Template)
			}
			st.mu.Lock()
			blob := st.blobs[v.TarballKey]
			st.mu.Unlock()
			if !bytes.Equal(blob, tb) {
				t.Errorf("stored blob differs from the uploaded tarball (%d vs %d bytes)", len(blob), len(tb))
			}
		})
	}
}

// A configured gate still receives the full Source bytes (unchanged contract)
// AND a working SourceOpener that re-opens the identical bytes on every call.
func TestDeployPolicyGateGetsSourceAndOpener(t *testing.T) {
	tb := gzTarball(t, map[string]string{"index.html": "<h1>hi</h1>"})
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	svc := newService(c, newFakeStore())
	svc.DeployTimeout = 50 * time.Millisecond
	gate := &capturePolicy{}
	svc.Policy = gate

	if _, err := svc.Deploy(context.Background(), Request{Name: "gated", Owner: "alice", Tarball: bytes.NewReader(tb)}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	in := gate.got()
	if !bytes.Equal(in.Source, tb) {
		t.Errorf("Input.Source (%d bytes) differs from the uploaded tarball (%d bytes)", len(in.Source), len(tb))
	}
	if in.SourceOpener == nil {
		t.Fatal("Input.SourceOpener must be populated on the buffered path")
	}
	for i := 0; i < 2; i++ { // each call must yield a fresh reader from the start
		rc, err := in.SourceOpener(context.Background())
		if err != nil {
			t.Fatalf("SourceOpener call %d: %v", i, err)
		}
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read opened source (call %d): %v", i, err)
		}
		if !bytes.Equal(got, tb) {
			t.Errorf("SourceOpener call %d yielded %d bytes, want the %d-byte tarball", i, len(got), len(tb))
		}
	}
}

// streamSource fails with byte-identical errors to readCapped for oversized
// and empty input, on both the seekable and the spooled branch.
func TestStreamSourceMatchesReadCappedErrors(t *testing.T) {
	const max = int64(8)
	over := []byte("123456789") // 9 > 8

	capped := func(b []byte) string {
		t.Helper()
		_, err := readCapped(bytes.NewReader(b), max)
		if err == nil {
			t.Fatal("readCapped must fail")
		}
		return err.Error()
	}
	wantOver, wantEmpty := capped(over), capped(nil)

	readers := []struct {
		name string
		wrap func([]byte) io.Reader
	}{
		{"seekable", func(b []byte) io.Reader { return bytes.NewReader(b) }},
		{"non-seekable", func(b []byte) io.Reader { return nonSeeker{bytes.NewReader(b)} }},
	}
	for _, r := range readers {
		t.Run(r.name, func(t *testing.T) {
			if _, _, err := streamSource(r.wrap(over), sha256.New(), max); err == nil || err.Error() != wantOver {
				t.Errorf("oversize error = %v, want %q", err, wantOver)
			}
			if _, _, err := streamSource(r.wrap(nil), sha256.New(), max); err == nil || err.Error() != wantEmpty {
				t.Errorf("empty error = %v, want %q", err, wantEmpty)
			}
		})
	}
}

// End-to-end: an over-cap source on the streaming path is rejected with the
// same error the buffered path raised, and nothing is stored. The section
// reader synthesizes MaxTarballBytes+1 without allocating the payload.
func TestDeployStreamingEnforcesSizeCap(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	st := newFakeStore()
	svc := newService(c, st)

	_, err := svc.Deploy(context.Background(), Request{
		Name:    "big",
		Owner:   "alice",
		Tarball: io.NewSectionReader(zeroReaderAt{}, 0, MaxTarballBytes+1),
	})
	if err == nil || !strings.Contains(err.Error(), "source exceeds 50 MB limit") {
		t.Fatalf("err = %v, want the 50 MB cap error", err)
	}
	if st.puts() != 0 {
		t.Errorf("an over-cap deploy must not store source, got %d puts", st.puts())
	}
}

// The spool temp file is always removed — after a successful deploy, after a
// mid-deploy failure (classify), and after a failure inside streamSource
// itself (empty source).
func TestDeployStreamingLeavesNoSpoolResidue(t *testing.T) {
	spoolDir := t.TempDir()
	t.Setenv("TMPDIR", spoolDir) // os.CreateTemp("", ...) resolves per call

	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	svc := newService(c, newFakeStore())
	svc.DeployTimeout = 50 * time.Millisecond

	tb := gzTarball(t, map[string]string{"index.html": "x"})
	if _, err := svc.Deploy(context.Background(), Request{Name: "ok", Owner: "a", Tarball: nonSeeker{bytes.NewReader(tb)}}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	assertNoSpool(t, spoolDir, "successful deploy")

	if _, err := svc.Deploy(context.Background(), Request{Name: "bad", Owner: "a", Tarball: nonSeeker{strings.NewReader("not a gzip stream")}}); err == nil {
		t.Fatal("expected a classify error for non-gzip source")
	}
	assertNoSpool(t, spoolDir, "classify failure")

	if _, err := svc.Deploy(context.Background(), Request{Name: "empty", Owner: "a", Tarball: nonSeeker{bytes.NewReader(nil)}}); err == nil || !strings.Contains(err.Error(), "source is empty") {
		t.Fatalf("err = %v, want the empty-source error", err)
	}
	assertNoSpool(t, spoolDir, "empty source")
}

func assertNoSpool(t *testing.T, dir, after string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read spool dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "vibed-deploy-") {
			t.Errorf("leftover spool file %q after %s", e.Name(), after)
		}
	}
}

// Memory evidence for the O(1) claim: acquiring a seekable source via
// streamSource allocates a constant few hundred bytes regardless of payload
// size, while the buffered readCapped path allocates the full payload.
// Run with: go test -bench SourceAcquisition -benchmem ./internal/deploy/
func BenchmarkSourceAcquisitionStreaming(b *testing.B) {
	payload := bytes.Repeat([]byte{0xa5}, 8<<20)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		open, cleanup, err := streamSource(bytes.NewReader(payload), sha256.New(), MaxTarballBytes)
		if err != nil {
			b.Fatal(err)
		}
		_ = open
		cleanup()
	}
}

func BenchmarkSourceAcquisitionBuffered(b *testing.B) {
	payload := bytes.Repeat([]byte{0xa5}, 8<<20)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := readCapped(io.TeeReader(bytes.NewReader(payload), sha256.New()), MaxTarballBytes); err != nil {
			b.Fatal(err)
		}
	}
}
