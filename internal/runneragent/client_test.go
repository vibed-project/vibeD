package runneragent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newClientAgainstAgent wires a Client to a real Agent over httptest — the
// client and agent are exercised together.
func newClientAgainstAgent(t *testing.T, token string) *Client {
	t.Helper()
	a := New(Config{Workdir: t.TempDir(), Token: token, AppPort: 8080, StopGrace: time.Second})
	srv := httptest.NewServer(a.handler())
	t.Cleanup(srv.Close)
	t.Cleanup(func() { a.stopProcess() })
	return NewClient(srv.URL, token)
}

func TestClientInjectStatusLogsStop(t *testing.T) {
	c := newClientAgainstAgent(t, "tok")
	ctx := context.Background()

	if err := c.Healthz(ctx); err != nil {
		t.Fatalf("Healthz: %v", err)
	}

	st, err := c.Inject(ctx, InjectRequest{
		Command: []string{"sh", "-c", "echo client-test-line; sleep 30"},
		Files:   map[string]string{"marker": "x"},
	})
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if st.State != StateRunning {
		t.Fatalf("inject state = %q, want running", st.State)
	}

	st, err = c.Status(ctx)
	if err != nil || st.State != StateRunning {
		t.Fatalf("Status = %+v, err = %v", st, err)
	}

	// The user process's output is captured and retrievable.
	deadline := time.Now().Add(2 * time.Second)
	for {
		logs, err := c.Logs(ctx, 50)
		if err != nil {
			t.Fatalf("Logs: %v", err)
		}
		if len(logs.Lines) > 0 && strings.Contains(strings.Join(logs.Lines, "\n"), "client-test-line") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected captured log line, got %v", logs.Lines)
		}
		time.Sleep(20 * time.Millisecond)
	}

	st, err = c.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if st.State != StateIdle {
		t.Fatalf("stop state = %q, want idle", st.State)
	}
}

func TestClientRejectsBadToken(t *testing.T) {
	a := New(Config{Workdir: t.TempDir(), Token: "right", StopGrace: time.Second})
	srv := httptest.NewServer(a.handler())
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "wrong")
	if _, err := c.Status(context.Background()); err == nil {
		t.Fatal("Status with wrong token should error")
	}
	// Healthz is unauthenticated, so it still succeeds.
	if err := c.Healthz(context.Background()); err != nil {
		t.Fatalf("Healthz should be unauthenticated: %v", err)
	}
}

func TestClientSurfacesAgentError(t *testing.T) {
	c := newClientAgainstAgent(t, "")
	// Empty Files + no SourceURL → the agent rejects the inject with a 400.
	_, err := c.Inject(context.Background(), InjectRequest{Language: "python"})
	if err == nil {
		t.Fatal("Inject with no files or source_url should error")
	}
	if !strings.Contains(err.Error(), "files or source_url") {
		t.Errorf("error %q should surface the agent's message", err)
	}
}
