package tarsafe

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestLimitedReader_WithinBudget(t *testing.T) {
	lr := NewLimitedReader(strings.NewReader("hello world"), 100)
	got, err := io.ReadAll(lr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("got %q", got)
	}
}

func TestLimitedReader_ExactBudgetIsNotAnError(t *testing.T) {
	lr := NewLimitedReader(strings.NewReader("abcde"), 5)
	got, err := io.ReadAll(lr)
	if err != nil {
		t.Fatalf("reading exactly the budget must not error: %v", err)
	}
	if string(got) != "abcde" {
		t.Fatalf("got %q", got)
	}
}

func TestLimitedReader_OverBudgetFailsHardAndEarly(t *testing.T) {
	const budget = 1024
	// A 4 MiB source; a decompression bomb is analogous but larger.
	big := bytes.NewReader(make([]byte, 4<<20))
	lr := NewLimitedReader(big, budget)

	n, err := io.Copy(io.Discard, lr)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	// It must abort near the budget, not drain the whole source (bounded by the
	// budget plus at most one copy buffer).
	if n >= 4<<20 {
		t.Fatalf("reader drained the whole source (%d bytes); expected an early abort", n)
	}
}
