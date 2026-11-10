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
