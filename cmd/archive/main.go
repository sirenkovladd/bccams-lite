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
	"time"
)

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
	manifestCap    int
	statsDepth     int
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
		manifestCap:    getenvInt("MANIFEST_CAP", 100),
		statsDepth:     getenvInt("STATS_DEPTH", 20),
	}
	flag.BoolVar(&cfg.dryRun, "dry-run", false, "single fetch pass, no uploads")
	flag.BoolVar(&cfg.once, "once", false, "run one cycle then exit")
	flag.Parse()
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

func runCycle(cfg *config, st *daemonState, fetchClient *http.Client, r2 *r2Client) (ok, skipped, stored, errs int) {
	cycleStart := time.Now()
	var lines []any

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
				errs++
				lines = append(lines, cycleNote("spool-retry-failed", key+": "+err.Error()))
				continue
			}
			os.Remove(filepath.Join(cfg.spoolDir, e.Name()))
			stored++
		}
	}

	// 2. Poll every cam+direction, sequentially, continue-on-error.
	for _, cam := range cameras {
		for _, dir := range directions {
			key := cam.ID + "/" + dir
			fs := st.Frames[key]
			if fs == nil {
				fs = &frameState{}
				st.Frames[key] = fs
			}
			var since string
			if fs.LastModified != "" && !strings.HasPrefix(fs.LastModified, "local-") {
				since = fs.LastModified
			}
			fr := fetchFrame(fetchClient, cam.Dirs[dir], since, fs.ETag)
			line := checkLine{
				Type: "check", Ts: time.Now().UTC().Format(time.RFC3339),
				CamID: cam.ID, Dir: dir, URL: cam.Dirs[dir],
			}
			if fr.err != nil {
				msg := fr.err.Error()
				errs++
				line.HTTPStatus, line.Error, line.FetchMs = 0, strptr(msg), fr.fetchMs
				line.PrevSHA256, line.PrevUpdateAt = fs.LastHash, fs.PrevUpdateAt
				line.AvgIntervalMs, line.NextPredicted = fs.AvgIntervalMs, fs.NextPredicted
				lines = append(lines, line)
				continue
			}
			line.HTTPStatus, line.LastModified, line.ETag = fr.status, fr.lastMod, fr.etag
			line.ContentLength, line.SHA256, line.FetchMs = fr.contentLen, fr.hash, fr.fetchMs
			line.PrevSHA256 = fs.LastHash
			if fr.notModified || !isNew(fr.lastMod, fr.hash, fs) {
				skipped++
				line.Changed = false
				line.PrevUpdateAt, line.AvgIntervalMs, line.NextPredicted =
					fs.PrevUpdateAt, fs.AvgIntervalMs, fs.NextPredicted
				lines = append(lines, line)
				continue
			}
			// New frame.
			now := time.Now()
			ts := fileName(fr.lastMod)
			r2key := fmt.Sprintf("history/%s/%s/%s.jpg", cam.ID, dir, ts)
			if cfg.dryRun || r2 == nil {
				ok++
				stored++
				line.Changed = true
				recordUpdate(fs, now, cfg.statsDepth)
				fs.LastModified, fs.ETag, fs.LastHash, fs.LastFile = fr.lastMod, fr.etag, fr.hash, ts
				insertKey(fs, r2key, cfg.manifestCap)
				line.PrevUpdateAt, line.AvgIntervalMs, line.NextPredicted =
					fs.PrevUpdateAt, fs.AvgIntervalMs, fs.NextPredicted
				lines = append(lines, line)
				continue
			}
			uploadMs, err := r2.put(r2key, "image/jpeg", "public, max-age=31536000, immutable", fr.body)
			line.UploadMs = uploadMs
			if err != nil {
				// Spool to disk, keep going; state still advances so the next
				// poll compares against the new frame, not the old one.
				os.MkdirAll(cfg.spoolDir, 0o755)
				sp := spoolFile(cfg.spoolDir, cam.ID, dir, ts)
				if werr := os.WriteFile(sp, fr.body, 0o644); werr != nil {
					msg := "upload failed and spool failed: " + err.Error() + " / " + werr.Error()
					errs++
					line.Error = strptr(msg)
					lines = append(lines, line)
					continue
				}
				msg := "upload failed, spooled: " + err.Error()
				errs++
				line.Error = strptr(msg)
				recordUpdate(fs, now, cfg.statsDepth)
				fs.LastModified, fs.ETag, fs.LastHash, fs.LastFile = fr.lastMod, fr.etag, fr.hash, ts
				insertKey(fs, r2key, cfg.manifestCap)
				line.Changed = true
				line.PrevUpdateAt, line.AvgIntervalMs, line.NextPredicted =
					fs.PrevUpdateAt, fs.AvgIntervalMs, fs.NextPredicted
				lines = append(lines, line)
				continue
			}
			ok++
			stored++
			line.Changed = true
			line.StoredKey = strptr(r2key)
			recordUpdate(fs, now, cfg.statsDepth)
			fs.LastModified, fs.ETag, fs.LastHash, fs.LastFile = fr.lastMod, fr.etag, fr.hash, ts
			insertKey(fs, r2key, cfg.manifestCap)
			line.PrevUpdateAt, line.AvgIntervalMs, line.NextPredicted =
				fs.PrevUpdateAt, fs.AvgIntervalMs, fs.NextPredicted
			lines = append(lines, line)
		}
	}

	// 3. Prune keys older than retention.
	now := time.Now()
	for _, old := range prune(st, time.Duration(cfg.retentionHours)*time.Hour, now) {
		if cfg.dryRun || r2 == nil {
			continue
		}
		if err := r2.del(old); err != nil {
			errs++
			lines = append(lines, cycleNote("prune-failed", old+": "+err.Error()))
		}
	}

	// 4. Publish manifest + stats (skipped in dry-run).
	if !cfg.dryRun && r2 != nil {
		m := buildManifest(st, cfg)
		mb, _ := json.Marshal(m)
		stats := map[string]any{"generatedAt": m.GeneratedAt, "stats": m.Stats, "frames": st.Frames}
		sb, _ := json.MarshalIndent(stats, "", "  ")
		if _, err := r2.put("history/manifest.json", "application/json", "public, max-age=60", mb); err != nil {
			errs++
			lines = append(lines, cycleNote("manifest-failed", err.Error()))
		}
		if _, err := r2.put("history/stats.json", "application/json", "public, max-age=60", sb); err != nil {
			errs++
			lines = append(lines, cycleNote("stats-failed", err.Error()))
		}
	}

	// 5. Persist state + flush log batch (always, including dry-run).
	if err := saveState(cfg.statePath, st); err != nil {
		errs++
		lines = append(lines, cycleNote("state-save-failed", err.Error()))
	}
	cycleMs := time.Since(cycleStart).Milliseconds()
	lines = append(lines, cycleLine{Type: "cycle", Ts: now.Format(time.RFC3339),
		OK: ok, Skipped: skipped, Stored: stored, Errors: errs, CycleMs: cycleMs, DryRun: cfg.dryRun})
	if err := appendLog(cfg.logPath, lines); err != nil {
		fmt.Fprintf(os.Stderr, "log append failed: %v\n", err)
	}
	fmt.Printf("%s ok=%d skipped=%d stored=%d errors=%d cycleMs=%d\n",
		now.UTC().Format(time.RFC3339), ok, skipped, stored, errs, cycleMs)
	return ok, skipped, stored, errs
}

func cycleNote(kind, msg string) map[string]string {
	return map[string]string{"type": kind, "ts": time.Now().UTC().Format(time.RFC3339), "msg": msg}
}

// insertKey appends a stored key and keeps the list in Last-Modified (key-time)
// order, newest-last, so manifest order never depends on fetch order.
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
	runCycle(cfg, st, fetchClient, r2)
	if cfg.once || cfg.dryRun {
		return
	}
	ticker := time.NewTicker(time.Duration(cfg.pollIntervalMs) * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		runCycle(cfg, st, fetchClient, r2)
	}
}
