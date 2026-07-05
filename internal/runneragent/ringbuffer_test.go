package runneragent

import (
	"reflect"
	"testing"
)

func TestRingBufferLinesAndCapacity(t *testing.T) {
	r := newRingBuffer(3)
	r.Write([]byte("a\nb\nc\nd\n"))
	got := r.snapshot(0)
	want := []string{"b", "c", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot = %v, want %v", got, want)
	}
}

func TestRingBufferPartialLine(t *testing.T) {
	r := newRingBuffer(10)
	r.Write([]byte("hel"))
	r.Write([]byte("lo\nwor"))
	got := r.snapshot(0)
	want := []string{"hello", "wor"} // trailing partial included
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot = %v, want %v", got, want)
	}
}

func TestRingBufferSnapshotN(t *testing.T) {
	r := newRingBuffer(10)
	r.Write([]byte("1\n2\n3\n4\n5\n"))
	if got := r.snapshot(2); !reflect.DeepEqual(got, []string{"4", "5"}) {
		t.Fatalf("snapshot(2) = %v, want [4 5]", got)
	}
}

func TestRingBufferReset(t *testing.T) {
	r := newRingBuffer(10)
	r.Write([]byte("x\ny"))
	r.reset()
	if got := r.snapshot(0); len(got) != 0 {
		t.Fatalf("after reset snapshot = %v, want empty", got)
	}
}

// TestRingBufferWraparound writes far more lines than capacity and confirms the
// circular buffer keeps exactly the most recent `capacity` lines in order
// (exercises the O(1) overwrite/wrap path, #79).
func TestRingBufferWraparound(t *testing.T) {
	r := newRingBuffer(3)
	for i := 1; i <= 100; i++ {
		r.Write([]byte(itoa(i) + "\n"))
	}
	got := r.snapshot(0)
	want := []string{"98", "99", "100"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after 100 lines, snapshot = %v, want %v", got, want)
	}

	// reset then reuse must produce a clean, correctly-ordered buffer.
	r.reset()
	r.Write([]byte("a\nb\nc\nd\n"))
	if got := r.snapshot(0); !reflect.DeepEqual(got, []string{"b", "c", "d"}) {
		t.Fatalf("after reset+reuse, snapshot = %v, want [b c d]", got)
	}
}
