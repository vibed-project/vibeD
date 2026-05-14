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
