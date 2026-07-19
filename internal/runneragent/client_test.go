package runneragent

import (
	"context"
	"errors"
	"net/http"
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

func TestClientHonorsCallerContextDeadline(t *testing.T) {
	// A slow agent: the response arrives ~600ms after the request. Whether a
	// call survives that must be decided by the caller's ctx, not by a hidden
	// client-side cap (the old whole-request Timeout silently truncated any
	// caller deadline above 30s, cancelling legitimately slow cold-start
	// injects).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return on client abort so srv.Close (which waits for in-flight
		// handlers) doesn't stall on the short-deadline request below.
		select {
		case <-r.Context().Done():
			return
		case <-time.After(600 * time.Millisecond):
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"idle"}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "")

	// Guard the fix directly: a whole-request Timeout would reintroduce the
	// silent truncation no matter what the transport is configured with.
	if c.hc.Timeout != 0 {
		t.Fatalf("client Timeout = %v, want 0 (caller ctx must govern)", c.hc.Timeout)
	}

	// A deadline above the server delay succeeds — no hidden cap below the ctx.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if st, err := c.Status(ctx); err != nil || st.State != StateIdle {
		t.Fatalf("Status with 2s deadline = %+v, err = %v", st, err)
	}

	// A deadline below the server delay fails promptly with the ctx's error —
	// the caller's deadline, not the client, governs total request duration.
	shortCtx, cancel2 := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel2()
	start := time.Now()
	_, err := c.Status(shortCtx)
	if err == nil {
		t.Fatal("Status with 150ms deadline should fail")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded in the chain", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("deadline failure took %v, want ~150ms", elapsed)
	}
}

func TestClientInfo(t *testing.T) {
	c := newClientAgainstAgent(t, "tok")
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	// The agent always reports the contract version it implements; /info is
	// reachable without auth and round-trips into InfoResponse.
	if info.AgentContract != AgentContract {
		t.Errorf("agentContract = %q, want %q", info.AgentContract, AgentContract)
	}
}
