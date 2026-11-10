package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"time"
)

// Per cam+direction archive state.
type frameState struct {
	LastModified  string   `json:"lastModified"`
	ETag          string   `json:"etag,omitempty"`
	LastHash      string   `json:"lastHash"`
	LastFile      string   `json:"lastFile"`
	Intervals     []int64  `json:"intervals"`
	AvgIntervalMs int64    `json:"avgIntervalMs"`
	NextPredicted string   `json:"nextPredictedAt"`
	PrevUpdateAt  string   `json:"prevUpdateAt"`
	StoredKeys    []string `json:"storedKeys"`
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// isNew votes by Last-Modified header ("" when absent) with SHA-256 tiebreak.
func isNew(lastModified, hash string, st *frameState) bool {
	if st == nil {
		return true
	}
	return lastModified != st.LastModified || hash != st.LastHash
}

// fileName derives the object timestamp from the Last-Modified header,
// falling back to local UTC with a "local-" prefix when the header is absent.
func fileName(lastModified string) string {
	if lastModified != "" {
		if t, err := time.Parse(http.TimeFormat, lastModified); err == nil {
			return t.UTC().Format("2006-01-02T15-04-05")
		}
	}
	return "local-" + time.Now().UTC().Format("2006-01-02T15-04-05")
}

type fetchResult struct {
	status      int
	lastMod     string
	etag        string
	contentLen  int64
	body        []byte
	hash        string
	fetchMs     int64
	notModified bool
	err         error
}

// fetchFrame performs a conditional GET (15s timeout). 304 → notModified.
func fetchFrame(client *http.Client, url, since, etag string) fetchResult {
	start := time.Now()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fetchResult{err: err}
	}
	if since != "" {
		req.Header.Set("If-Modified-Since", since)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	res, err := client.Do(req)
	if err != nil {
		return fetchResult{err: err, fetchMs: time.Since(start).Milliseconds()}
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotModified {
		return fetchResult{status: 304, notModified: true, fetchMs: time.Since(start).Milliseconds()}
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 20<<20))
	if err != nil {
		return fetchResult{status: res.StatusCode, err: err, fetchMs: time.Since(start).Milliseconds()}
	}
	lm := res.Header.Get("Last-Modified")
	return fetchResult{
		status:     res.StatusCode,
		lastMod:    lm,
		etag:       res.Header.Get("ETag"),
		contentLen: res.ContentLength,
		body:       body,
		hash:       sha256Hex(body),
		fetchMs:    time.Since(start).Milliseconds(),
	}
}

// recordUpdate folds a detected update into cadence stats.
func recordUpdate(st *frameState, now time.Time, statsDepth int) {
	if st.PrevUpdateAt != "" {
		if prev, err := time.Parse(time.RFC3339, st.PrevUpdateAt); err == nil {
			delta := now.Sub(prev).Milliseconds()
			if delta > 0 {
				st.Intervals = append(st.Intervals, delta)
				if len(st.Intervals) > statsDepth {
					st.Intervals = st.Intervals[len(st.Intervals)-statsDepth:]
				}
				var sum int64
				for _, d := range st.Intervals {
					sum += d
				}
				st.AvgIntervalMs = sum / int64(len(st.Intervals))
				st.NextPredicted = now.Add(time.Duration(st.AvgIntervalMs) * time.Millisecond).UTC().Format(time.RFC3339)
			}
		}
	}
	st.PrevUpdateAt = now.UTC().Format(time.RFC3339)
}
