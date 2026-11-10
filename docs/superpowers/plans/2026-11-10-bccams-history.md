# bccams-lite v1 + History Archiver Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the static traffic-cam viewer AND the Go archiver daemon that stores rolling 24h history in Cloudflare R2, with a DuckDB-queryable log.

**Architecture:** `scripts/build.mjs` (bun) verifies 16 live JPEGs and writes `data/cams.json`; `public/` vanilla JS + Leaflet reads `cams.json` and (when configured) `history/manifest.json` from R2. `cmd/archive` (Go, no cgo, stdlib-first) polls every 5 min, dedupes by `Last-Modified` + SHA-256, uploads new frames to R2, appends fsync'd JSONL log lines, prunes to retention. VPS daemon is egress-only — no inbound ports, no public API. Deploy = sync `public/` to the public R2 bucket.

**Tech Stack:** Bun (build), vanilla JS + Leaflet 1.9.4 (viewer), Go stdlib (archiver), Cloudflare R2 S3-compatible (storage + static hosting), DuckDB CLI (operator-side analytics on the JSONL log).

**Spec:** `docs/superpowers/specs/2026-11-10-history-archiver-design.md` (approved) + `PROMPT.md` (viewer acceptance). This plan supersedes the earlier static-only draft in this same directory.

## Global Constraints

- Static viewer: no backend in request path, no image proxy, no framework build step; `public/index.html` < 100KB; images `loading="lazy"`; works from `python3 -m http.server -d public` with no console errors.
- Never invent image URLs — only `data/seed.json` + URLs scraped from `trafficcams.vancouver.ca` page HTML (`cameraimages/*.jpg`, absolute prefix `https://trafficcams.vancouver.ca/cameraimages/`).
- Camera record shape: `{id, name, neighbourhood, page, lat, lng, images:{N,E,S,W}}`; exact ids `burrard-davie`, `bute-davie`, `burrard-drake`, `burrard-pacific`.
- Burrard & Pacific S still is `BurrardBridgeSouth.jpg` (asymmetric — do not "fix").
- Archiver: Go, no cgo, RSS target < 50 MB, `GOMEMLIMIT` in systemd unit; R2 credentials via env only, never in repo; log/state always safe to pull while running (JSONL append + fsync per batch; `state.json` via tmp + fsync + rename).
- Temp captures/probe scripts in `$TMPDIR`, never the repo. `grep -ri secret public data scripts cmd` clean of values (env *names* like `R2_SECRET_ACCESS_KEY` appear only in `.env.example`/docs).

## Review Focus

- `Last-Modified` missing on some responses → filename falls back to local UTC with `local-` prefix and hash is the newness vote; test pins this path with a synthetic header-less response.
- City re-encodes identical scenes (same bytes? different bytes?) → byte SHA-256 may store visual duplicates; acceptable v1, but the per-check log keeps both hashes so duplicates are auditable via DuckDB.
- R2 outage mid-cycle → frames spool to local disk queue, manifest still published for successes, retry next cycle; test pins spool + retry without manifest truncation.
- Viewer with no R2 configured (Pages without archive, `HISTORY_BASE_URL` empty) must show live + session strip with zero console errors; test pins empty-config path.
- Clock skew between VPS and R2/CDN → history ordering uses header-derived timestamps, never local clock alone; test asserts manifest order follows `Last-Modified`, not fetch order.

---

### Task 1: Build script + verified camera data

**Files:**
- Create: `scripts/build.mjs`
- Create (generated, committed): `data/cams.json`

**Interfaces:**
- Consumes: `data/seed.json` (`mustInclude[2]` must match emitted filenames exactly).
- Produces: `data/cams.json` — 4 objects `{id, name, neighbourhood, page, lat, lng, images:{N,E,S,W}}`; consumed by `public/app.js` (`fetch('cams.json')`) and mirrored by the Go archiver table (Task 4 checks drift).

- [ ] **Step 1: Write the camera table into `scripts/build.mjs`**

  Exact 4 records (URLs verbatim, prefix `https://trafficcams.vancouver.ca/cameraimages/`):
  - `burrard-davie` | "Burrard St & Davie St" | West End | page `https://trafficcams.vancouver.ca/burrardDavie.htm` | 49.2791,-123.1292 | N `BurrardDavieNorth.jpg` E `BurrardDavieEast.jpg` S `BurrardDavieSouth.jpg` W `BurrardDavieWest.jpg`
  - `bute-davie` | "Bute St & Davie St" | West End | page `https://trafficcams.vancouver.ca/buteDavie.htm` | 49.2818,-123.1334 | N `ButeNorthDavie.jpg` E `DavieEastBute.jpg` S `ButeSouthDavie.jpg` W `DavieWestBute.jpg`
  - `burrard-drake` | "Burrard St & Drake St" | West End | page `https://trafficcams.vancouver.ca/burrardDrake.htm` | 49.2809,-123.1289 | N `BurrardDrakeNorth.jpg` E `BurrardDrakeEast.jpg` S `BurrardDrakeSouth.jpg` W `BurrardDrakeWest.jpg`
  - `burrard-pacific` | "Burrard St & Pacific Ave" | West End | page `https://trafficcams.vancouver.ca/BurrardPacific4.htm` | 49.2775,-123.1285 | N `BurrardPacificNorth.jpg` E `BurrardPacificEast.jpg` S `BurrardBridgeSouth.jpg` W `BurrardPacificWest.jpg`

- [ ] **Step 2: Implement verification + write**

  `bun scripts/build.mjs` → per URL `HEAD`, fallback GET on 405; assert 200 + `content-type` starts `image/`; any failure prints URL + status and `process.exit(1)`; success writes pretty `data/cams.json` + prints `wrote data/cams.json (4 cams, 16 images verified)`.

- [ ] **Step 3: Run the build**

  Run: `bun scripts/build.mjs`
  Expected: exit 0; `data/cams.json` has 4 entries each with N/E/S/W https URLs.

- [ ] **Step 4: Commit**

  ```bash
  git add scripts/build.mjs data/cams.json
  git commit -m "feat: add verified build script and 4-cam dataset"
  ```

### Task 2: Static shell (index.html + style.css)

**Files:**
- Create: `public/index.html`
- Create: `public/style.css`
- Create: `public/history.json` (pointer file: `{"base": ""}` empty = archive disabled; deploy overwrites with R2 public base)

**Interfaces:**
- Consumes: `data/cams.json` shape (Task 1).
- Produces: DOM contract for Task 3: `#map`, `#search`, `#hood-filter`, `#cam-list`, `#count`, `#modal[hidden]` > `#modal-title`, `#modal-stills` (4× `figure > img[loading=lazy] + figcaption` + `<time>`), `#frame-strip`, `#modal-close`. `history.json` consumed by Task 3 (`fetch('history.json')` → `fetch('<base>/history/manifest.json')` when base non-empty).

- [ ] **Step 1: Write `public/index.html` skeleton**

  Header "bccams-lite" + source link-back note; controls (`#search` placeholder "Search intersections…", `#hood-filter` "All neighbourhoods" + "West End"); main `#map` + `#cam-list`; `#count`; modal as above; Leaflet 1.9.4 CSS in head, Leaflet JS + `app.js` deferred. No inline image URLs. Target < 10KB.

- [ ] **Step 2: Write `public/style.css`**

  Mobile-first: header, controls row, two-column (list 320px + map) → single column under 800px; modal overlay, stills 2×2 → 1 col; frame strip horizontal thumbs. No framework/fonts.

- [ ] **Step 3: Verify size and clean load**

  Run: `wc -c public/index.html && python3 -m http.server -d public 8123` → open, check console empty.
  Expected: < 102400 bytes; no console errors.

- [ ] **Step 4: Commit**

  ```bash
  git add public/index.html public/style.css public/history.json
  git commit -m "feat: add static shell with map, list, and modal"
  ```

### Task 3: Frontend app (app.js, live + R2 history)

**Files:**
- Create: `public/app.js`

**Interfaces:**
- Consumes: Task 2 DOM + `fetch('cams.json')`, `fetch('history.json')`.
- Produces: rendered pins/list; modal with live refresh + history strip. No exports.

- [ ] **Step 1: Write list/map/search/filter**

  Fetch `cams.json` → render `#cam-list` buttons (name + neighbourhood), Leaflet pins (`if (window.L)` guard) with name popups; `#search` substring case-insensitive; `#hood-filter` by neighbourhood; `#count` "N cams". Click list/pin → modal.

- [ ] **Step 2: Write modal + live refresh + session strip**

  4 stills N/E/S/W `loading="lazy"` + `<time>` "updated HH:MM:SS"; 60s `setInterval` re-fetch with `?t=Date.now()` cache-bust; in-memory per-cam ring buffer last 6 → `#frame-strip`; `localStorage bccams-lite:lastSeen:<id>` timestamp only. Close via button/overlay/Escape; clear interval on close. Empty `history.json` base → this strip is the history (no errors).

- [ ] **Step 3: Write R2 history enhancement**

  When base non-empty: `fetch('<base>/history/manifest.json', {cache:'no-store'})` → replace strip with manifest thumbs for the cam (lazy, click-to-large), newest-last; on fetch failure keep session strip and log one `console.warn` (no error). Manifest entry URLs joined against base.

- [ ] **Step 4: Verify in browser**

  Run: `python3 -m http.server -d public 8123`; click "Burrard St & Davie St" → 4 stills; search "bute" → 1 result; console clean.
  Expected: pass; `cams.json` + 4 JPEGs in network tab.

- [ ] **Step 5: Commit**

  ```bash
  git add public/app.js
  git commit -m "feat: add viewer app with search, pins, refresh, R2 history"
  ```

### Task 4: Go archiver daemon

**Files:**
- Create: `go.mod` (root, module `bccams-lite`, go ≥ 1.22), `cmd/archive/main.go`, `cmd/archive/*.go` (split by responsibility, e.g. `poll.go`, `r2.go`, `store.go` — names builder's call, layout stated in commit)
- Create: `.env.example` (placeholder values only), `deploy/archive.service` (systemd, `EnvironmentFile=`, `GOMEMLIMIT`)
- NOT committed: real `.env`, `./data/` runtime files, `archive/` spool

**Interfaces:**
- Consumes: cam table mirror of Task 1 (builder states shared-generation vs duplicate + startup drift check against `data/cams.json`: fail-fast on mismatch).
- Produces: R2 objects `history/<cam>/<DIR>/<ts>.jpg` + `history/manifest.json` + `history/stats.json` (shapes per design); local `STATE_PATH` JSON + append-only `LOG_PATH` JSONL (field list per design, fsync'd per batch).

- [ ] **Step 1: Write config + cam table + poll loop skeleton**

  Env: `POLL_INTERVAL_MS` (def 300000), `RETENTION_HOURS` (def 24), `R2_ACCOUNT_ID`, `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`, `R2_BUCKET`, `R2_PUBLIC_BASE_URL`, `R2_ENDPOINT` (opt), `LOG_PATH` (def `./data/archive-log.jsonl`), `STATE_PATH` (def `./data/archive-state.json`), `MANIFEST_CAP` (def 100), `STATS_DEPTH` (def 20). `GET` with 15s timeout, conditional headers from state, sequential, continue-on-error, 60s-cycle log line `ok/skipped/stored/errors`.

- [ ] **Step 2: Write newness + cadence logic**

  On 200: `lm = Last-Modified` (fallback `local-<utc>`); `hash = sha256(body)`; new iff `lm != lastModified || hash != lastHash`. On new: record `delta = now - prevUpdateAt`, push to intervals (cap depth), `avg`, `nextPredictedAt = lastUpdate + avg`; filename `<ts from lm>.jpg`. Emit per-check JSONL line with all 17 design fields.

- [ ] **Step 3: Write R2 uploader (stdlib-first SigV4) + prune + state**

  PUT object (`image/jpeg`, long cache), PUT `manifest.json`/`stats.json` (`max-age=60`), DELETE keys older than retention, spool failures to local dir and retry next cycle. State via tmp + fsync + rename. Manifest shape exactly: `{generatedAt, retentionHours, pollIntervalMs, publicBase, cams:{id:{N:[...],E:[...],S:[...],W:[...]}}, stats:{"id/N":{lastModified, avgIntervalMs, nextPredictedAt}}}`.

- [ ] **Step 4: Write unit tests for newness, ordering, and spool retry**

  `go test ./...` covers: header-less response falls back to `local-<utc>` filename with hash as newness vote; manifest direction lists follow `Last-Modified` order not fetch order; failed upload spools locally and a later success publishes without truncating prior manifest entries.
  Expected: all tests pass.

- [ ] **Step 5: Write `.env.example`, systemd unit, and a dry-run check**

  `LOG_PATH=$TMPDIR` dry run: `go vet ./... && go build ./cmd/archive && POLL_INTERVAL_MS=60000 RETENTION_HOURS=24 ./archive --once --dry-run` (both flags required; single cycle, no uploads, no R2 credentials needed).
  Expected: exit 0, JSONL lines in tmp log, no uploads.

- [ ] **Step 6: Commit**

  ```bash
  git add go.mod go.sum cmd/archive .env.example deploy/archive.service
  git commit -m "feat: add Go archiver with R2 upload and JSONL log"
  ```

### Task 5: R2 deploy, README, acceptance gate

**Files:**
- Modify: `README.md` (deploy + DuckDB analytics section)

**Interfaces:**
- Consumes: Tasks 1–4.
- Produces: documented deploy + gate result.

- [ ] **Step 1: Document deploy + analytics in `README.md`**

  Exact commands: `bun scripts/build.mjs`; `go build ./cmd/archive`; static sync to R2 (state the one tool, e.g. `aws s3 sync public/ s3://$R2_BUCKET/ --endpoint-url $R2_ENDPOINT`); `history.json` base wiring; VPS systemd enable; R2 bucket CORS snippet; DuckDB examples: `SELECT camId, dir, count() FROM read_json('archive-log.jsonl') WHERE changed GROUP BY 1,2;` and gap query. Note: no inbound ports on VPS.

- [ ] **Step 2: Run the full acceptance gate**

  ```bash
  bun scripts/build.mjs
  go vet ./... && go build ./cmd/archive
  ls -la public/ && wc -c public/index.html
  grep -ri secret public data scripts cmd --exclude-dir=.git || echo "no secrets"
  python3 -m http.server -d public 8123
  ```
  Expected: build exit 0; `index.html` < 102400; grep clean of values; Burrard & Davie + Bute & Davie N/E/S/W present; manual click shows 4 stills + refresh + strip.

- [ ] **Step 3: Commit**

  ```bash
  git add README.md
  git commit -m "docs: document R2 deploy, VPS archiver, and acceptance gate"
  ```
