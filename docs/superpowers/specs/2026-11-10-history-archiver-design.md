# History Archiver (Option B) — Design Doc

## Intent / why
- Source (`trafficcams.vancouver.ca`) serves only the latest JPEG per direction, updated ~every 10–15 min, with no archive.
- Goal: one tiny archiver on the VPS polls on a schedule, keeps a frame only when it is actually newer, stores images + history in Cloudflare R2, and the static viewer reads history from R2 when present.
- Why B: viewer stays static (free Cloudflare Pages for live-only); history is a progressive enhancement served from R2.

## Goals
- VPS daemon (Go, single static binary, systemd unit) polls all v1 cams (4 cams × N/E/S/W = 16 JPEGs) every 5 min (env-overridable; source cadence is 10–15 min so 5-min catches each update with ~one skipped cycle).
- Store a frame only when actually newer: primary signal is `Last-Modified` response header (observed to equal capture time burned into the image); fallback/tiebreak is SHA-256 byte compare. Honor `ETag`/`If-Modified-Since` conditional GETs.
- Cadence learning per cam+direction: on each detected update, log the delta vs previous update (`cam dir updated after Δ; avg Δ; predicted next`), persist stats so the daemon knows the per-camera rhythm and can pull just before the predicted update. v1: fixed 5-min loop + stats/prediction in logs and manifest; adaptive per-cam scheduling is a later step, not v1.
- R2 layout: timestamped immutable JPEGs + a small `manifest.json` (+ per-cam stats) the static viewer fetches from the R2 public base URL with CORS.
- Rolling retention default **24h (last day)**, set via env `RETENTION_HOURS`.
- Viewer: history strip reads R2 manifest when configured/present, falls back to in-memory last-6 otherwise.
- No secrets in repo; all R2 credentials via env.
- Minimal RAM: Go with no cgo, stdlib-first (HTTP + SigV4 for R2 S3-compatible API); any S3 library must be justified, RSS target < 50 MB with `GOMEMLIMIT` in the systemd unit.

## Non-goals / anti-goals
- No full backend API, no DB, no auth, no per-user history.
- No committing archived JPEGs to git.
- Anti-goal: re-saving identical bytes every cycle and filling R2 (dedupe is mandatory).
- Anti-goal: breaking static deploy — `public/` must still work with zero archiver/R2 present.
- Anti-goal: adaptive polling complexity in v1 (stats first, adaptive later).

## Constraints (from repo)
- Static viewer: `public/` + `data/cams.json`, no build step, `index.html` < 100KB, lazy images, no console errors.
- `scripts/build.mjs` still verifies live origin URLs; archiver reuses the same cam table (builder states shared-module vs duplicate+check).
- Temp/probe output in `$TMPDIR`, never the repo. `grep -ri secret public data scripts` stays clean (env names like `R2_SECRET_ACCESS_KEY` contain "secret" in name only — builder must name the grep exception or use `R2_SECRET_ACCESS_KEY` solely in `.env.example`/docs, never a real value; acceptance grep stays clean of values).

## Approaches considered
- **A. Static only:** in-memory last-6 + localStorage timestamps. Free, but history dies on reload. Rejected.
- **B. Static viewer + VPS archiver to R2 (PICKED):** daemon polls 5 min, Last-Modified + hash dedupe, writes timestamped JPEGs + `manifest.json` to R2, prunes to retention. Viewer enhances when manifest exists.
- **C. Full backend (API + DB):** real queries/long retention, but kills free static hosting. Rejected for v1.

## Design (picked approach)

### Components
1. **`cmd/archive` (Go daemon on VPS, single static binary under systemd; `scripts/build.mjs` stays Bun per PROMPT)**
   - Reads cam table (same 4 cams / 16 URLs as build).
   - Loop every `POLL_INTERVAL_MS` (default 5 min), sequential fetches, ~15s per-fetch timeout, continue-on-error.
   - Per cam+direction state (persisted to R2 as `stats.json` + local `STATE_PATH` mirror): `lastModified`, `lastHash`, `lastFile`, `intervals[]` (last ~20 update deltas), `avgIntervalMs`, `nextPredictedAt`.
   - Per cycle per frame: conditional GET (`If-Modified-Since: lastModified`, `If-None-Match`); on 304 → skip. On 200: compare `Last-Modified` to stored; if same AND sha256 == lastHash → skip. Else store.
   - On store: log `cam dir updated after Δ (avg Δ, predicted next at T)`; update stats; upload JPEG + patched manifest/stats.
   - Prune each cycle: delete R2 objects older than `RETENTION_HOURS`; rewrite manifest.
   - Cycle log line: `ts ok=N skipped=N stored=N errors=N`.

2. **R2 layout (bucket `BCCAMS_BUCKET`, public base `R2_PUBLIC_BASE_URL`)**
   - `history/<cam-id>/<DIR>/YYYY-MM-DDTHH-MM-SS.jpg` (UTC, immutable).
   - `history/manifest.json`: `{ generatedAt, retentionHours, pollIntervalMs, publicBase, cams: { "<id>": { "N": ["history/<id>/N/<ts>.jpg", ... newest-last, capped 100], ... } }, stats: { "<id>/N": { lastModified, avgIntervalMs, nextPredictedAt } } }`.
   - `history/stats.json`: full per-cam+direction interval history (cap last 20 deltas each).
   - CORS on bucket allows GET from the Pages/box origins. Cache: JPEGs immutable long-cache; `manifest.json`/`stats.json` short-cache (`max-age=60`).
   - Estimated volume after 5-min dedupe: source changes ~every 10–15 min → ~100–150 new files/day across 16 frames; 24h retention ≈ that many live objects. Trivial for R2.

3. **Config (env, `.env.example` committed with placeholders only)**
   - `POLL_INTERVAL_MS` (default 300000), `RETENTION_HOURS` (default 24), `R2_ACCOUNT_ID`, `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`, `R2_BUCKET`, `R2_PUBLIC_BASE_URL`, optional `R2_ENDPOINT` override.
   - Systemd unit file `deploy/archive.service` (examples with `EnvironmentFile=`) — builder's call on exact path, must document.

4. **Serving**
   - Viewer is unchanged statically: `public/` on Pages or box.
   - History comes from `R2_PUBLIC_BASE_URL/history/manifest.json` (URL baked at build via `HISTORY_BASE_URL` env or runtime `history.json` pointer — builder states which, one only).
   - R2-absent/unconfigured → viewer shows live + session-only strip (current plan behavior).

5. **Viewer wiring (later plan task)**
   - `app.js` modal strip prefers manifest entries (lazy thumbs, click-to-large), falls back to in-memory ring buffer.
   - Live stills cache-busted with `?t=` on refresh; R2 history URLs immutable (long cache OK).

### Cadence learning (v1 scope)
- Data comes from real `Last-Modified` deltas (equals capture time per operator observation) plus hash-change confirmation.
- v1 outputs: per-cycle log deltas, `avgIntervalMs` + `nextPredictedAt` per cam+direction in manifest/stats.
- Explicitly NOT v1: variable per-cam timers; the loop stays fixed 5-min. Prediction exists so a later change can wake early per cam.

### Failure modes
- 4xx/5xx/timeout/304 handling: keep last frame, log, retry next cycle; never truncate manifest on transient errors.
- Partial cycle → still publish manifest for what succeeded.
- Clock skew: timestamps from `Last-Modified` header, not local clock, for filenames (fallback local UTC with `local-` prefix if header missing).
- R2 outage → local spool to `$TMPDIR`/disk queue, retry upload; viewer keeps serving last manifest.

## Serving topology (approved)
- `server -> [upload] -> R2 storage`; `visitor -> R2 storage -> image`. The VPS daemon is egress-only: it polls the city site and uploads to R2, and exposes **no public API / no inbound ports**.
- Static site (`public/` HTML/CSS/JS + `data/cams.json`) is also deployed to the same public R2 bucket — deploy = build + sync files to R2. No server process in the request path.

## Analytics log (approved: DuckDB over MySQL)
- Daemon appends one JSON object per line to `LOG_PATH` (default `./data/archive-log.jsonl`), **fsync'd every batch** so the file is always safe to pull/copy even while the daemon runs (append-only + atomic `state.json` via tmp+fsync+rename).
- No DB engine in the daemon (keeps RAM minimal). The operator analyzes with DuckDB CLI directly on the pulled file: `SELECT * FROM read_json('archive-log.jsonl') WHERE changed;` — DuckDB natively reads JSONL (and CSV). MySQL rejected: needs a second server + RAM for no v1 benefit.
- Per-check line fields (details for double-checking): `type:"check"`, `ts`, `camId`, `dir`, `url`, `httpStatus`, `lastModified`, `etag`, `contentLength`, `sha256`, `prevSha256`, `changed`, `storedKey|null`, `fetchMs`, `uploadMs`, `error|null`, `prevUpdateAt`, `avgIntervalMs`, `nextPredictedAt`.
- Per-cycle summary line: `type:"cycle"`, `ts`, `ok`, `skipped`, `stored`, `errors`, `cycleMs`.
- README documents example DuckDB queries (updates-per-day per cam, missed-update gaps, fetch-error rate).

## Left to the builder
- Shared cam-table module vs duplicate + consistency check (must not drift from `data/cams.json`).
- Exact `HISTORY_BASE_URL` wiring (build-time env vs runtime pointer file) — one mechanism only.
- Manifest per-direction cap (proposed 100) and stats depth (proposed last 20 deltas) as constants.
- Go module layout (`go.mod` at root, `cmd/archive/main.go`), S3 client choice (stdlib-only SigV4 vs minimal library), `go build` release flags.
- systemd unit + README deploy steps for VPS (`go build ./cmd/archive` → single binary); R2 bucket + CORS + lifecycle-note documentation (lifecycle optional since daemon prunes).
