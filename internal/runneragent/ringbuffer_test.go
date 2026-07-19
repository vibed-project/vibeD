package runneragent

import (
	"reflect"
	"strconv"
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

// TestRingBufferWrapAround exercises the circular buffer well past capacity
// (issue #79): after many evictions it must still hold exactly the last
// `capacity` lines, in order, regardless of how many times start wrapped.
func TestRingBufferWrapAround(t *testing.T) {
	r := newRingBuffer(3)
	for i := 0; i < 100; i++ {
		r.Write([]byte(strconv.Itoa(i) + "\n"))
	}
	got := r.snapshot(0)
	want := []string{"97", "98", "99"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after 100 writes snapshot = %v, want %v", got, want)
	}
	// snapshot(n) larger than the live count returns everything, no panic.
	if got := r.snapshot(10); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot(10) = %v, want %v", got, want)
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
