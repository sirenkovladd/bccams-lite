package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var directions = []string{"N", "E", "S", "W"}

type config struct {
	pollIntervalMs int64
	retentionHours int
	accountID      string
	accessKey      string
	secretKey      string
	bucket         string
	publicBase     string
	endpoint       string
	logPath        string
	statePath      string
	spoolDir       string
	camsJSON       string
	fetchWorkers   int
	manifestCap    int
	statsDepth     int
	placeholder    map[string]bool
	dryRun         bool
	once           bool
}

func getenvInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getenvInt64(name string, def int64) int64 {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

// City's "temporarily out of service" card (300x205 static JPEG, verified
// 2026-11-10: 98% placeholder-blue, byte-identical across cams). Frames with
// these hashes are never stored or published. Extend without a rebuild via
// PLACEHOLDER_SHA256S (comma-separated).
var defaultPlaceholders = map[string]bool{
	"dd16c59eb9dafb660a6bbef0d62547da5250e6310b736982c63fd784edae2c1a": true,
}

func placeholderSet() map[string]bool {
	out := map[string]bool{}
	for h := range defaultPlaceholders {
		out[h] = true
	}
	for _, h := range strings.Split(os.Getenv("PLACEHOLDER_SHA256S"), ",") {
		if h = strings.TrimSpace(h); h != "" {
			out[h] = true
		}
	}
	return out
}

func loadConfig() *config {
	cfg := &config{
		pollIntervalMs: getenvInt64("POLL_INTERVAL_MS", 300000),
		retentionHours: getenvInt("RETENTION_HOURS", 24),
		accountID:      os.Getenv("R2_ACCOUNT_ID"),
		accessKey:      os.Getenv("R2_ACCESS_KEY_ID"),
		secretKey:      os.Getenv("R2_SECRET_ACCESS_KEY"),
		bucket:         os.Getenv("R2_BUCKET"),
		publicBase:     os.Getenv("R2_PUBLIC_BASE_URL"),
		endpoint:       os.Getenv("R2_ENDPOINT"),
		logPath:        firstNonEmpty(os.Getenv("LOG_PATH"), "./data/archive-log.jsonl"),
		statePath:      firstNonEmpty(os.Getenv("STATE_PATH"), "./data/archive-state.json"),
		camsJSON:       firstNonEmpty(os.Getenv("CAMS_JSON"), "./data/cams.json"),
		fetchWorkers:   getenvInt("FETCH_WORKERS", 16),
		manifestCap:    getenvInt("MANIFEST_CAP", 100),
		placeholder:    placeholderSet(),
		statsDepth:     getenvInt("STATS_DEPTH", 20),
	}
	flag.BoolVar(&cfg.dryRun, "dry-run", false, "single fetch pass, no uploads")
	flag.BoolVar(&cfg.once, "once", false, "run one cycle then exit")
	flag.Parse()
	if cfg.fetchWorkers < 1 {
		cfg.fetchWorkers = 1
	}
	return cfg
}

func firstNonEmpty(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func (cfg *config) r2() *r2Client {
	endpoint := cfg.endpoint
	if endpoint == "" {
		endpoint = "https://" + cfg.accountID + ".r2.cloudflarestorage.com"
	}
	return &r2Client{
		http:     &http.Client{Timeout: 30 * time.Second},
		endpoint: endpoint,
		bucket:   cfg.bucket,
		region:   "auto",
		access:   cfg.accessKey,
		secret:   cfg.secretKey,
	}
}

func strptr(s string) *string { return &s }

// runPool fans jobs out over n workers, collecting results in job order.
func runPool[T any, R any](jobs []T, n int, fn func(T) R) []R {
	out := make([]R, len(jobs))
	var mu sync.Mutex
	next := 0
	var wg sync.WaitGroup
	for w := 0; w < n && w < len(jobs); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				i := next
				next++
				mu.Unlock()
				if i >= len(jobs) {
					return
				}
				out[i] = fn(jobs[i])
			}
		}()
	}
	wg.Wait()
	return out
}

type frameJob struct {
	cam camera
	dir string
	fs  *frameState
}

type cycle struct {
	cfg   *config
	st    *daemonState
	r2    *r2Client
	cams  []camera
	lines []any
	dirty map[string]bool
	ok    int
	skip  int
	store int
	errs  int
}

func frameStateFor(st *daemonState, camID, dir string) *frameState {
	key := camID + "/" + dir
	fs := st.Frames[key]
	if fs == nil {
		fs = &frameState{}
		st.Frames[key] = fs
	}
	return fs
}

func runCycle(cfg *config, st *daemonState, fetchClient *http.Client, r2 *r2Client, cams []camera) (ok, skipped, stored, errs int) {
	cycleStart := time.Now()
	cy := &cycle{cfg: cfg, st: st, r2: r2, cams: cams, dirty: map[string]bool{}}

	// 1. Retry spooled uploads from previous failures (skipped in dry-run).
	if !cfg.dryRun && r2 != nil {
		entries, _ := os.ReadDir(cfg.spoolDir)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			key, good := spoolKey(e.Name())
			if !good {
				continue
			}
			body, err := os.ReadFile(filepath.Join(cfg.spoolDir, e.Name()))
			if err != nil {
				continue
			}
			if _, err := r2.put(key, "image/jpeg", "public, max-age=31536000, immutable", body); err != nil {
				cy.errs++
				cy.lines = append(cy.lines, cycleNote("spool-retry-failed", key+": "+err.Error()))
				continue
			}
			os.Remove(filepath.Join(cfg.spoolDir, e.Name()))
			cy.store++
		}
	}

	// 2. Fetch every cam+dir over the worker pool (read-only on state), then
	// apply results sequentially in cam/dir order (deterministic logs).
	var jobs []frameJob
	for _, cam := range cams {
		for _, dir := range directions {
			if cam.Images[dir] == "" {
				continue
			}
			jobs = append(jobs, frameJob{cam: cam, dir: dir, fs: frameStateFor(st, cam.ID, dir)})
		}
	}
	fetchFn := func(j frameJob) fetchResult {
		var since string
		if j.fs.LastModified != "" && !strings.HasPrefix(j.fs.LastModified, "local-") {
			since = j.fs.LastModified
		}
		return fetchFrame(fetchClient, j.cam.Images[j.dir], since, j.fs.ETag)
	}
	results := runPool(jobs, cfg.fetchWorkers, fetchFn)
	for i, j := range jobs {
		cy.applyFrame(j, results[i])
	}

	// 3. Prune keys older than retention.
	now := time.Now()
	pruned := prune(st, time.Duration(cfg.retentionHours)*time.Hour, now)
	if !(cfg.dryRun || r2 == nil) {
		for _, old := range pruned {
			if err := r2.del(old); err != nil {
				cy.errs++
				cy.lines = append(cy.lines, cycleNote("prune-failed", old+": "+err.Error()))
			}
		}
	}

	// 4. Publish slim top-level manifest + dirty per-cam shards + stats.
	// Skipped entirely on quiet cycles (saves R2 Class A ops at full-city scale).
	if !(cfg.dryRun || r2 == nil) && (len(cy.dirty) > 0 || len(pruned) > 0) {
		m := buildManifest(st, cfg, cams)
		mb, _ := json.Marshal(m)
		if _, err := r2.put("history/manifest.json", "application/json", "public, max-age=60", mb); err != nil {
			cy.errs++
			cy.lines = append(cy.lines, cycleNote("manifest-failed", err.Error()))
		}
		for camID := range cy.dirty {
			sh := buildCamShard(st, cfg, camID)
			sb, _ := json.Marshal(sh)
			if _, err := r2.put("history/"+camID+"/manifest.json", "application/json", "public, max-age=60", sb); err != nil {
				cy.errs++
				cy.lines = append(cy.lines, cycleNote("shard-failed", camID+": "+err.Error()))
			}
		}
		// NOTE: no history/stats.json upload. It carried the full Frames map
		// (megabytes per dirty cycle at full-city scale) and nothing reads it:
		// the viewer uses per-cam shards, operators use STATE_PATH + the JSONL log.
	}

	// 5. Persist state + flush log batch (always, including dry-run).
	if err := saveState(cfg.statePath, st); err != nil {
		cy.errs++
		cy.lines = append(cy.lines, cycleNote("state-save-failed", err.Error()))
	}
	cycleMs := time.Since(cycleStart).Milliseconds()
	cy.lines = append(cy.lines, cycleLine{Type: "cycle", Ts: now.Format(time.RFC3339),
		OK: cy.ok, Skipped: cy.skip, Stored: cy.store, Errors: cy.errs, CycleMs: cycleMs,
		DryRun: cfg.dryRun, Workers: cfg.fetchWorkers})
	if cycleMs > cfg.pollIntervalMs {
		msg := fmt.Sprintf("cycle %dms overran interval %dms", cycleMs, cfg.pollIntervalMs)
		fmt.Fprintln(os.Stderr, "WARN: "+msg)
		cy.lines = append(cy.lines, cycleNote("cycle-overrun", msg))
	}
	if err := appendLog(cfg.logPath, cy.lines); err != nil {
		fmt.Fprintf(os.Stderr, "log append failed: %v\n", err)
	}
	fmt.Printf("%s ok=%d skipped=%d stored=%d errors=%d cycleMs=%d workers=%d\n",
		now.UTC().Format(time.RFC3339), cy.ok, cy.skip, cy.store, cy.errs, cycleMs, cfg.fetchWorkers)
	return cy.ok, cy.skip, cy.store, cy.errs
}

func (cy *cycle) applyFrame(j frameJob, fr fetchResult) {
	fs := j.fs
	cam, dir := j.cam, j.dir
	line := checkLine{
		Type: "check", Ts: time.Now().UTC().Format(time.RFC3339),
		CamID: cam.ID, Dir: dir, URL: cam.Images[dir],
	}
	if fr.err != nil {
		msg := fr.err.Error()
		cy.errs++
		line.HTTPStatus, line.Error, line.FetchMs = 0, strptr(msg), fr.fetchMs
		line.PrevSHA256, line.PrevUpdateAt = fs.LastHash, fs.PrevUpdateAt
		line.AvgIntervalMs, line.NextPredicted = fs.AvgIntervalMs, fs.NextPredicted
		cy.lines = append(cy.lines, line)
		return
	}
	line.HTTPStatus, line.LastModified, line.ETag = fr.status, fr.lastMod, fr.etag
	line.ContentLength, line.SHA256, line.FetchMs = fr.contentLen, fr.hash, fr.fetchMs
	line.PrevSHA256 = fs.LastHash
	if fr.notModified || !isNew(fr.lastMod, fr.hash, fs) {
		cy.skip++
		line.Changed = false
		line.PrevUpdateAt, line.AvgIntervalMs, line.NextPredicted =
			fs.PrevUpdateAt, fs.AvgIntervalMs, fs.NextPredicted
		cy.lines = append(cy.lines, line)
		return
	}
	// New frame.
	if cy.cfg.placeholder[fr.hash] {
		// Out-of-service card: never stored, never published, state untouched
		// (the cam stays "new" so recovery is picked up on the next cycle).
		cy.skip++
		line.Changed = false
		line.PrevUpdateAt, line.AvgIntervalMs, line.NextPredicted =
			fs.PrevUpdateAt, fs.AvgIntervalMs, fs.NextPredicted
		cy.lines = append(cy.lines, line)
		cy.lines = append(cy.lines, cycleNote("placeholder-skipped", cam.ID+"/"+dir))
		return
	}
	now := time.Now()
	ts := fileName(fr.lastMod)
	r2key := fmt.Sprintf("history/%s/%s/%s.jpg", cam.ID, dir, ts)
	cfg, r2 := cy.cfg, cy.r2
	if cfg.dryRun || r2 == nil {
		// Report-only: nothing was uploaded, so record nothing. Recording
		// here persists phantom keys that later publish as dangling
		// manifest entries (viewer renders them as blank stills).
		cy.ok++
		cy.store++
		line.Changed = true
		line.PrevUpdateAt, line.AvgIntervalMs, line.NextPredicted =
			fs.PrevUpdateAt, fs.AvgIntervalMs, fs.NextPredicted
		cy.lines = append(cy.lines, line)
		return
	}
	uploadMs, err := r2.put(r2key, "image/jpeg", "public, max-age=31536000, immutable", fr.body)
	line.UploadMs = uploadMs
	advance := func(spooled bool, msg string) {
		if !spooled {
			cy.ok++
			line.StoredKey = strptr(r2key)
		} else {
			cy.errs++
			line.Error = strptr(msg)
		}
		cy.store++
		recordUpdate(fs, now, cfg.statsDepth)
		fs.LastModified, fs.ETag, fs.LastHash, fs.LastFile = fr.lastMod, fr.etag, fr.hash, ts
		insertKey(fs, r2key, cfg.manifestCap)
		cy.dirty[cam.ID] = true
		line.Changed = true
		line.PrevUpdateAt, line.AvgIntervalMs, line.NextPredicted =
			fs.PrevUpdateAt, fs.AvgIntervalMs, fs.NextPredicted
		cy.lines = append(cy.lines, line)
	}
	if err != nil {
		// Spool to disk, keep going; state still advances so the next
		// poll compares against the new frame, not the old one.
		os.MkdirAll(cfg.spoolDir, 0o755)
		sp := spoolFile(cfg.spoolDir, cam.ID, dir, ts)
		if werr := os.WriteFile(sp, fr.body, 0o644); werr != nil {
			msg := "upload failed and spool failed: " + err.Error() + " / " + werr.Error()
			cy.errs++
			line.Error = strptr(msg)
			cy.lines = append(cy.lines, line)
			return
		}
		advance(true, "upload failed, spooled: "+err.Error())
		return
	}
	advance(false, "")
}

func cycleNote(kind, msg string) map[string]string {
	return map[string]string{"type": kind, "ts": time.Now().UTC().Format(time.RFC3339), "msg": msg}
}

func insertKey(fs *frameState, key string, cap int) {
	fs.StoredKeys = append(fs.StoredKeys, key)
	sort.SliceStable(fs.StoredKeys, func(i, j int) bool {
		return keyTime(fs.StoredKeys[i]).Before(keyTime(fs.StoredKeys[j]))
	})
	capKeys(fs, cap)
}

func capKeys(fs *frameState, cap int) {
	if cap > 0 && len(fs.StoredKeys) > cap {
		fs.StoredKeys = fs.StoredKeys[len(fs.StoredKeys)-cap:]
	}
}

func main() {
	cfg := loadConfig()
	if cfg.spoolDir == "" {
		cfg.spoolDir = filepath.Join(filepath.Dir(cfg.statePath), "spool")
	}
	cams, err := loadCams(cfg.camsJSON)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(2)
	}
	ndirs := 0
	for _, c := range cams {
		ndirs += len(c.Images)
	}
	fmt.Fprintf(os.Stderr, "loaded %d cams (%d dirs) from %s\n", len(cams), ndirs, cfg.camsJSON)
	st := loadState(cfg.statePath)
	fetchClient := &http.Client{Timeout: 15 * time.Second}
	var r2 *r2Client
	if !cfg.dryRun {
		if cfg.accountID == "" || cfg.accessKey == "" || cfg.secretKey == "" || cfg.bucket == "" {
			fmt.Fprintln(os.Stderr, "missing R2 env (R2_ACCOUNT_ID / R2_ACCESS_KEY_ID / R2_SECRET_ACCESS_KEY / R2_BUCKET)")
			os.Exit(2)
		}
		r2 = cfg.r2()
	}
	runCycle(cfg, st, fetchClient, r2, cams)
	if cfg.once || cfg.dryRun {
		return
	}
	ticker := time.NewTicker(time.Duration(cfg.pollIntervalMs) * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		runCycle(cfg, st, fetchClient, r2, cams)
	}
}
