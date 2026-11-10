package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Per-cam shard direction lists follow Last-Modified order, not fetch order,
// and pre-existing entries survive new appends (no truncation on publish).
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
	m := buildCamShard(st, cfg, "burrard-davie")
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

// At full-city scale the top-level manifest stays slim and shards stay small.
func TestManifestSizes(t *testing.T) {
	st := &daemonState{Frames: map[string]*frameState{}}
	var cams []camera
	for i := 0; i < 220; i++ {
		id := fmt.Sprintf("cam%03d", i)
		images := map[string]string{}
		for _, d := range directions {
			images[d] = "https://x/" + d + ".jpg"
			key := id + "/" + d
			fs := &frameState{}
			for k := 0; k < 100; k++ {
				insertKey(fs, fmt.Sprintf("history/%s/%s/2026-11-10T%02d-%02d-00.jpg", id, d, k/60, k%60), 100)
			}
			st.Frames[key] = fs
		}
		cams = append(cams, camera{ID: id, Images: images})
	}
	cfg := &config{retentionHours: 24, pollIntervalMs: 300000, publicBase: "https://r2/x"}
	top, _ := json.Marshal(buildManifest(st, cfg, cams))
	if len(top) > 100*1024 {
		t.Fatalf("top-level manifest too big: %d bytes", len(top))
	}
	shard, _ := json.Marshal(buildCamShard(st, cfg, "cam000"))
	if len(shard) > 50*1024 {
		t.Fatalf("per-cam shard too big: %d bytes", len(shard))
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

// Worker pool visits every job exactly once, results in job order.
func TestRunPool(t *testing.T) {
	jobs := make([]int, 0, 876)
	for i := 0; i < 876; i++ {
		jobs = append(jobs, i)
	}
	var mu sync.Mutex
	seen := map[int]int{}
	out := runPool(jobs, 16, func(j int) int {
		mu.Lock()
		seen[j]++
		mu.Unlock()
		return j * 2
	})
	if len(out) != len(jobs) {
		t.Fatalf("got %d results, want %d", len(out), len(jobs))
	}
	for i, v := range out {
		if v != jobs[i]*2 {
			t.Fatalf("result %d out of order: %d", i, v)
		}
		if seen[i] != 1 {
			t.Fatalf("job %d visited %d times", i, seen[i])
		}
	}
}
