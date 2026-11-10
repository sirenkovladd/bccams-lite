package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Manifest direction lists follow Last-Modified order, not fetch order, and
// pre-existing entries survive new appends (no truncation on publish).
func TestManifestOrderAndNoTruncation(t *testing.T) {
	st := &daemonState{Frames: map[string]*frameState{}}
	fs := &frameState{}
	st.Frames["burrard-davie/N"] = fs
	for _, k := range []string{
		"history/burrard-davie/N/2026-11-10T08-30-00.jpg",
		"history/burrard-davie/N/2026-11-10T08-00-00.jpg", // out of order
		"history/burrard-davie/N/2026-11-10T08-15-00.jpg",
	} {
		insertKey(fs, k, 100)
	}
	cfg := &config{retentionHours: 24, pollIntervalMs: 300000, manifestCap: 100}
	m := buildManifest(st, cfg)
	got := m.Cams["burrard-davie"]["N"]
	want := []string{
		"history/burrard-davie/N/2026-11-10T08-00-00.jpg",
		"history/burrard-davie/N/2026-11-10T08-15-00.jpg",
		"history/burrard-davie/N/2026-11-10T08-30-00.jpg",
	}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("not Last-Modified ordered: %v", got)
	}
}

// prune drops only keys older than retention.
func TestPrune(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	fs := &frameState{StoredKeys: []string{
		"history/x/N/2026-11-10T11-30-00.jpg",
		"history/x/N/2026-11-09T10-00-00.jpg", // older than 24h
	}}
	st := &daemonState{Frames: map[string]*frameState{"x/N": fs}}
	removed := prune(st, 24*time.Hour, now)
	if len(removed) != 1 || len(fs.StoredKeys) != 1 {
		t.Fatalf("removed=%v kept=%v", removed, fs.StoredKeys)
	}
}

// Spool filenames round-trip to R2 keys.
func TestSpoolRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := spoolFile(dir, "burrard-davie", "N", "2026-11-10T08-15-00")
	if err := os.WriteFile(p, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	key, good := spoolKey(filepath.Base(p))
	if !good || key != "history/burrard-davie/N/2026-11-10T08-15-00.jpg" {
		t.Fatalf("key=%q good=%v", key, good)
	}
	if _, good := spoolKey("junk.txt"); good {
		t.Fatal("non-spool file accepted")
	}
}

// R2 PUT/DELETE against a stub server: signing path works, errors surface.
func TestR2PutDel(t *testing.T) {
	var lastAuth, lastSHA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuth, lastSHA = r.Header.Get("Authorization"), r.Header.Get("X-Amz-Content-Sha256")
		if r.Method == "PUT" && r.URL.Path == "/b/fail.jpg" {
			http.Error(w, "boom", 500)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	c := &r2Client{http: srv.Client(), endpoint: srv.URL, bucket: "b",
		region: "auto", access: "AK", secret: "SK"}
	if _, err := c.put("fail.jpg", "image/jpeg", "", []byte("x")); err == nil {
		t.Fatal("expected error on HTTP 500")
	}
	if _, err := c.put("ok.jpg", "image/jpeg", "public, max-age=60", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if lastAuth == "" || lastSHA == "" {
		t.Fatal("missing SigV4 headers")
	}
	if err := c.del("gone.jpg"); err != nil {
		t.Fatal(err)
	}
}

// Go cam table must not drift from data/cams.json (the build's source of truth).
func TestCamTableMatchesCamsJSON(t *testing.T) {
	raw, err := os.ReadFile("../../data/cams.json")
	if err != nil {
		t.Skip("data/cams.json not present")
	}
	var disk []struct {
		ID     string            `json:"id"`
		Name   string            `json:"name"`
		Hood   string            `json:"neighbourhood"`
		Page   string            `json:"page"`
		Lat    float64           `json:"lat"`
		Lng    float64           `json:"lng"`
		Images map[string]string `json:"images"`
	}
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	if len(disk) != len(cameras) {
		t.Fatalf("cams.json has %d cams, Go table has %d", len(disk), len(cameras))
	}
	byID := map[string]camera{}
	for _, c := range cameras {
		byID[c.ID] = c
	}
	for _, d := range disk {
		g, ok := byID[d.ID]
		if !ok {
			t.Fatalf("cam %s missing from Go table", d.ID)
		}
		if g.Name != d.Name || g.Hood != d.Hood || g.Page != d.Page || g.Lat != d.Lat || g.Lng != d.Lng {
			t.Fatalf("cam %s metadata drift", d.ID)
		}
		for _, dir := range directions {
			if g.Dirs[dir] != d.Images[dir] {
				t.Fatalf("cam %s dir %s drift: go=%q disk=%q", d.ID, dir, g.Dirs[dir], d.Images[dir])
			}
		}
	}
}
