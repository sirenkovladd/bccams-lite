package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Publish uploads the slim top-level manifest + dirty per-cam shards, never
// the full daemon state: at full-city scale history/stats.json would be
// megabytes per dirty cycle and nothing reads it (the viewer uses per-cam
// shards; operators use STATE_PATH and the JSONL log).
func TestPublishSkipsStatsJSON(t *testing.T) {
	var mu sync.Mutex
	var puts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			mu.Lock()
			puts = append(puts, strings.TrimPrefix(r.URL.Path, "/b/"))
			mu.Unlock()
			w.WriteHeader(200)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Last-Modified", "Tue, 10 Nov 2026 12:00:00 GMT")
		_, _ = w.Write([]byte("fakejpegbytes"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := &config{
		pollIntervalMs: 300000,
		retentionHours: 24,
		logPath:        filepath.Join(dir, "log.jsonl"),
		statePath:      filepath.Join(dir, "state.json"),
		spoolDir:       filepath.Join(dir, "spool"),
		fetchWorkers:   2,
		manifestCap:    100,
	}
	r2 := &r2Client{http: srv.Client(), endpoint: srv.URL, bucket: "b",
		region: "auto", access: "AK", secret: "SK"}
	st := &daemonState{Frames: map[string]*frameState{}}
	cams := []camera{{ID: "testcam", Images: map[string]string{"N": srv.URL + "/n.jpg"}}}

	ok, _, stored, errs := runCycle(cfg, st, srv.Client(), r2, cams)
	if errs != 0 || ok != 1 || stored != 1 {
		t.Fatalf("cycle: ok=%d stored=%d errs=%d puts=%v", ok, stored, errs, puts)
	}
	has := func(key string) bool {
		for _, k := range puts {
			if k == key {
				return true
			}
		}
		return false
	}
	if !has("history/manifest.json") {
		t.Errorf("missing slim top-level manifest upload; puts=%v", puts)
	}
	if !has("history/testcam/manifest.json") {
		t.Errorf("missing per-cam shard upload; puts=%v", puts)
	}
	if has("history/stats.json") {
		t.Errorf("stats.json uploaded (full Frames map, nothing reads it); puts=%v", puts)
	}
}

// The city's "temporarily out of service" card must never be stored or
// published: it would spam every cam's history with identical blue frames.
func TestPlaceholderSkipped(t *testing.T) {
	body := []byte("fake-out-of-service-bytes")
	hash := sha256Hex(body)

	newCfg := func() *config {
		dir := t.TempDir()
		return &config{
			pollIntervalMs: 300000,
			retentionHours: 24,
			logPath:        filepath.Join(dir, "log.jsonl"),
			statePath:      filepath.Join(dir, "state.json"),
			spoolDir:       filepath.Join(dir, "spool"),
			fetchWorkers:   2,
			manifestCap:    100,
			placeholder:    map[string]bool{hash: true},
		}
	}
	newR2 := func(srv *httptest.Server, puts *[]string, mu *sync.Mutex) *r2Client {
		return &r2Client{http: srv.Client(), endpoint: srv.URL, bucket: "b",
			region: "auto", access: "AK", secret: "SK"}
	}
	serve := func(w http.ResponseWriter, r *http.Request) {}

	// Case 1: placeholder frame -> no PUT anywhere, counted as skipped.
	var mu sync.Mutex
	var puts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			mu.Lock()
			puts = append(puts, strings.TrimPrefix(r.URL.Path, "/b/"))
			mu.Unlock()
			w.WriteHeader(200)
			return
		}
		_ = serve
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Last-Modified", "Tue, 10 Nov 2026 12:00:00 GMT")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	cfg := newCfg()
	r2 := newR2(srv, &puts, &mu)
	st := &daemonState{Frames: map[string]*frameState{}}
	cams := []camera{{ID: "testcam", Images: map[string]string{"N": srv.URL + "/n.jpg"}}}
	ok, skipped, stored, errs := runCycle(cfg, st, srv.Client(), r2, cams)
	if errs != 0 || ok != 0 || stored != 0 || skipped != 1 {
		t.Fatalf("placeholder: ok=%d skipped=%d stored=%d errs=%d", ok, skipped, stored, errs)
	}
	if len(puts) != 0 {
		t.Fatalf("placeholder frame was uploaded: %v", puts)
	}

	// Case 2: same bytes, no placeholder rule -> stored and published.
	var puts2 []string
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			mu.Lock()
			puts2 = append(puts2, strings.TrimPrefix(r.URL.Path, "/b/"))
			mu.Unlock()
			w.WriteHeader(200)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Last-Modified", "Tue, 10 Nov 2026 12:00:00 GMT")
		_, _ = w.Write(body)
	}))
	defer srv2.Close()
	cfg2 := newCfg()
	cfg2.placeholder = map[string]bool{}
	r2b := &r2Client{http: srv2.Client(), endpoint: srv2.URL, bucket: "b",
		region: "auto", access: "AK", secret: "SK"}
	st2 := &daemonState{Frames: map[string]*frameState{}}
	cams2 := []camera{{ID: "testcam", Images: map[string]string{"N": srv2.URL + "/n.jpg"}}}
	ok, skipped, stored, errs = runCycle(cfg2, st2, srv2.Client(), r2b, cams2)
	if errs != 0 || ok != 1 || stored != 1 {
		t.Fatalf("control: ok=%d skipped=%d stored=%d errs=%d", ok, skipped, stored, errs)
	}
	if len(puts2) == 0 {
		t.Fatal("control frame was not uploaded")
	}
}

// The pinned fingerprint is the verified static placeholder (300x205 JPEG,
// 98% placeholder-blue, byte-identical across cams as of 2026-11-10).
func TestDefaultPlaceholderPinned(t *testing.T) {
	const want = "dd16c59eb9dafb660a6bbef0d62547da5250e6310b736982c63fd784edae2c1a"
	if !defaultPlaceholders[want] {
		t.Fatal("verified out-of-service fingerprint missing from defaults")
	}
}

// Dry runs (and nil-R2 runs) must not record state: every recorded-but-never-
// uploaded key becomes a phantom manifest entry pointing at a 404, which the
// viewer then renders as a blank still. Regression: an early dry-run poisoned
// state.json with keys that later published as dangling manifest entries.
func TestDryRunLeavesStateUntouched(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Last-Modified", "Tue, 10 Nov 2026 12:00:00 GMT")
		_, _ = w.Write([]byte("fakejpegbytes"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	cfg := &config{
		pollIntervalMs: 300000,
		retentionHours: 24,
		logPath:        filepath.Join(dir, "log.jsonl"),
		statePath:      filepath.Join(dir, "state.json"),
		spoolDir:       filepath.Join(dir, "spool"),
		fetchWorkers:   2,
		manifestCap:    100,
		dryRun:         true,
	}
	st := &daemonState{Frames: map[string]*frameState{}}
	cams := []camera{{ID: "testcam", Images: map[string]string{"N": srv.URL + "/n.jpg"}}}
	ok, _, stored, errs := runCycle(cfg, st, srv.Client(), nil, cams)
	if errs != 0 || ok != 1 || stored != 1 {
		t.Fatalf("dry-run report: ok=%d stored=%d errs=%d", ok, stored, errs)
	}
	for key, fs := range st.Frames {
		if len(fs.StoredKeys) != 0 || fs.LastHash != "" || fs.LastFile != "" {
			t.Fatalf("dry-run mutated state for %s: %+v", key, fs)
		}
	}
}
