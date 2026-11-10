package main

import "testing"

// Newness vote: Last-Modified header ("" when the response has none) plus
// SHA-256 tiebreak. Filename timestamp is derived separately.
func TestIsNew(t *testing.T) {
	h := sha256Hex([]byte("fake-jpeg-bytes"))
	st := &frameState{LastModified: "Tue, 10 Nov 2026 08:00:00 GMT", LastHash: "old"}

	if !isNew("Tue, 10 Nov 2026 08:15:00 GMT", h, st) {
		t.Fatal("new Last-Modified should be new")
	}
	if !isNew("Tue, 10 Nov 2026 08:00:00 GMT", h, st) {
		t.Fatal("same Last-Modified with different hash should be new (hash is the tiebreak)")
	}
	if isNew("Tue, 10 Nov 2026 08:00:00 GMT", "old", st) {
		t.Fatal("same Last-Modified and same hash should be skipped")
	}
	// Header-less fallback: empty sentinel on both sides, hash decides.
	st2 := &frameState{LastModified: "", LastHash: "old"}
	if !isNew("", h, st2) {
		t.Fatal("header-less fetch with different hash should be new")
	}
	if isNew("", "old", st2) {
		t.Fatal("header-less fetch with same hash should be skipped")
	}
}

func TestFileName(t *testing.T) {
	if got := fileName("Tue, 10 Nov 2026 08:15:00 GMT"); got != "2026-11-10T08-15-00" {
		t.Fatalf("header time parsed, got %q", got)
	}
	if got := fileName(""); got == "" || len(got) < len("local-") || got[:6] != "local-" {
		t.Fatalf("missing header falls back to local- prefix, got %q", got)
	}
}
