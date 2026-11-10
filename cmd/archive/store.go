package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Local persistent state (STATE_PATH). R2 mirrors the publishable subset.
type daemonState struct {
	Frames map[string]*frameState `json:"frames"`
}

func loadState(path string) *daemonState {
	st := &daemonState{Frames: map[string]*frameState{}}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return st
	}
	var raw daemonState
	if err := json.Unmarshal(b, &raw); err != nil || raw.Frames == nil {
		return st
	}
	return &raw
}

// saveState writes atomically (tmp + fsync + rename) so the file is always
// safe to pull while the daemon runs.
func saveState(path string, st *daemonState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// checkLine is one JSONL row per frame check — the DuckDB-queryable log.
type checkLine struct {
	Type          string  `json:"type"`
	Ts            string  `json:"ts"`
	CamID         string  `json:"camId"`
	Dir           string  `json:"dir"`
	URL           string  `json:"url"`
	HTTPStatus    int     `json:"httpStatus"`
	LastModified  string  `json:"lastModified"`
	ETag          string  `json:"etag"`
	ContentLength int64   `json:"contentLength"`
	SHA256        string  `json:"sha256"`
	PrevSHA256    string  `json:"prevSha256"`
	Changed       bool    `json:"changed"`
	StoredKey     *string `json:"storedKey"`
	FetchMs       int64   `json:"fetchMs"`
	UploadMs      int64   `json:"uploadMs"`
	Error         *string `json:"error"`
	PrevUpdateAt  string  `json:"prevUpdateAt"`
	AvgIntervalMs int64   `json:"avgIntervalMs"`
	NextPredicted string  `json:"nextPredictedAt"`
}

type cycleLine struct {
	Type    string `json:"type"`
	Ts      string `json:"ts"`
	OK      int    `json:"ok"`
	Skipped int    `json:"skipped"`
	Stored  int    `json:"stored"`
	Errors  int    `json:"errors"`
	CycleMs int64  `json:"cycleMs"`
	DryRun  bool   `json:"dryRun,omitempty"`
}

// appendLog appends lines and fsyncs once per batch.
func appendLog(path string, lines []any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			return err
		}
	}
	return f.Sync()
}

// manifest is the viewer-facing history index published to R2.
type manifest struct {
	GeneratedAt  string                         `json:"generatedAt"`
	RetentionHrs int                            `json:"retentionHours"`
	PollMs       int64                          `json:"pollIntervalMs"`
	PublicBase   string                         `json:"publicBase"`
	Cams         map[string]map[string][]string `json:"cams"`
	Stats        map[string]manifestStat        `json:"stats"`
}

type manifestStat struct {
	LastModified  string `json:"lastModified"`
	AvgIntervalMs int64  `json:"avgIntervalMs"`
	NextPredicted string `json:"nextPredictedAt"`
}

func buildManifest(st *daemonState, cfg *config) *manifest {
	m := &manifest{
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		RetentionHrs: cfg.retentionHours,
		PollMs:       cfg.pollIntervalMs,
		PublicBase:   cfg.publicBase,
		Cams:         map[string]map[string][]string{},
		Stats:        map[string]manifestStat{},
	}
	for _, cam := range cameras {
		perDir := map[string][]string{}
		for _, dir := range directions {
			key := cam.ID + "/" + dir
			fs := st.Frames[key]
			if fs == nil {
				perDir[dir] = []string{}
				continue
			}
			perDir[dir] = append([]string{}, fs.StoredKeys...)
			m.Stats[key] = manifestStat{
				LastModified:  fs.LastModified,
				AvgIntervalMs: fs.AvgIntervalMs,
				NextPredicted: fs.NextPredicted,
			}
		}
		m.Cams[cam.ID] = perDir
	}
	return m
}

// prune drops stored keys older than the retention window (oldest-first
// lists would need sorting; StoredKeys is kept newest-last, so filter it).
func prune(st *daemonState, retention time.Duration, now time.Time) []string {
	cutoff := now.Add(-retention)
	var removed []string
	for _, fs := range st.Frames {
		kept := fs.StoredKeys[:0]
		for _, k := range fs.StoredKeys {
			if keyTime(k).Before(cutoff) {
				removed = append(removed, k)
				continue
			}
			kept = append(kept, k)
		}
		// Fresh slice to avoid aliasing surprises.
		fs.StoredKeys = append([]string{}, kept...)
	}
	sort.Strings(removed)
	return removed
}

// keyTime parses history/<cam>/<DIR>/<ts>.jpg timestamps.
func keyTime(key string) time.Time {
	base := key[strings.LastIndex(key, "/")+1:]
	ts := strings.TrimSuffix(base, ".jpg")
	if t, err := time.Parse("2006-01-02T15-04-05", ts); err == nil {
		return t
	}
	if rest, ok := strings.CutPrefix(ts, "local-"); ok {
		if t, err := time.Parse("2006-01-02T15-04-05", rest); err == nil {
			return t
		}
	}
	return time.Time{}
}

// Spool: failed uploads wait on local disk; filenames encode the R2 key.
func spoolFile(dir, camID, direction, ts string) string {
	safe := fmt.Sprintf("%s__%s__%s.jpg", camID, direction, ts)
	return filepath.Join(dir, safe)
}

func spoolKey(name string) (string, bool) {
	if !strings.HasSuffix(name, ".jpg") {
		return "", false
	}
	parts := strings.Split(strings.TrimSuffix(name, ".jpg"), "__")
	if len(parts) != 3 {
		return "", false
	}
	return "history/" + parts[0] + "/" + parts[1] + "/" + parts[2] + ".jpg", true
}
