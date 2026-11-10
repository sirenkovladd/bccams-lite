# bccams-lite

Static, no-backend Vancouver traffic/weather camera viewer as plain HTML+JS, plus a tiny Go archiver that stores rolling history in Cloudflare R2. No PHP, no server in the request path.

## Why

- bccams.ca is the best aggregator but: anonymous owner, no contact page, `info@` bounces (`550 No Such User Here`), cameras missing, and `index.php` ships ~600KB of HTML with 388 feeds loading at once from HostGator.
- Known gap driving this: **Burrard St & Davie St** exists on the city site (`trafficcams.vancouver.ca/burrardDavie.htm`, 4 directions) but has 0 matches on bccams.ca.
- This repo proves the whole thing can be static files: free Cloudflare Pages, or any 512MB box with `caddy file-server` / nginx.

## Sources (all public JPEG stills, no keys)

- City intersections: `https://trafficcams.vancouver.ca` — updated ~every 10–15 min
- Highways/bridges: `https://www.drivebc.ca/cameras`
- UBC weather cams: `https://weather.eos.ubc.ca`

Hotlink the public JPEGs, link back to the source pages.

## Status

Full city: ~221 cams (219 city intersections from the city's KML map data +
UBC weather cam), verified live by `bun scripts/build.mjs` into `data/cams.json`
(+ runtime copy `public/cams.json`). Viewer is `public/` (vanilla JS + Leaflet):
region grid (bccams-style) + map tab + search. History archiver is `cmd/archive`
(Go, VPS systemd, worker pool, stores to R2). Seed data (verified URLs only) in
`data/seed.json`; region map in `data/regions.json`.

## Pipeline (city KML → build → grid/map viewer)

```
city KML (220 pins: id, name, page, coords)
  → scripts/build.mjs (scrapes pages, verifies every JPEG, assigns regions)
  → data/cams.json (+ public/ copy, data/skipped.json report)
  → public/ grid+map viewer  →  R2 static hosting
  → cmd/archive (reads CAMS_JSON, polls with worker pool)
  → history/<cam-id>/manifest.json shards + images in R2
```

Build skips unverifiable cams with reasons (never fails on one bad page) and
exits nonzero only if Burrard & Davie or Bute & Davie (the bccams.ca regression
cases) are missing. `KML_SOURCE=cache` replays the committed KML for testing.

## How it fits together

```
server (VPS, egress-only) --[poll every 5 min, upload new frames]--> R2 storage
visitor --[GET image / html / css / js]--> R2 storage (public bucket)
```

- The VPS daemon has **no public API and no inbound ports**: it polls the city
  site and uploads to R2. Deploy of the site itself is just syncing static
  files to the same public bucket.
- Without R2 configured, the viewer still works live-only (last-6 session strip).

## Deploy

```sh
# 1. Verify cameras + regenerate data (fails on any dead URL)
bun scripts/build.mjs

# 2. Point the viewer at history (or leave {"base": ""} for live-only)
echo '{"base": "https://pub-xxxxxxxxxxxxxxxx.r2.dev"}' > public/history.json

# 3a. Static hosting on R2 (wrangler with an admin API token; per-file content types)
for f in index.html app.js style.css cams.json history.json; do
  npx -y wrangler r2 object put "bccams-lite/$f" --file "public/$f" --remote
done
# 3b. or any static box:
python3 -m http.server -d public 80
# caddy file-server --root public --listen :80

# 4. Archiver on the VPS (Go, single binary)
go build -o /opt/bccams-lite/archive ./cmd/archive
cp .env.example /opt/bccams-lite/.env   # then fill in R2_* values
cp data/cams.json /opt/bccams-lite/data/cams.json  # CAMS_JSON source of truth
cp deploy/archive.service /etc/systemd/system/bccams-lite-archive.service
# 4b. Verify signing + upload against real R2 before enabling the timer:
set -a; source /opt/bccams-lite/.env; set +a
/tmp/archive --once   # expect exit 0; check history/manifest.json in the bucket
systemctl enable --now bccams-lite-archive
```

R2 volume at full-city scale: ~876 frames/cycle, ~7k new files/day, ~1–2GB live
at 24h retention (inside the free tier). Manifest PUTs stay small: the slim
top-level manifest plus only the per-cam shards that changed each cycle
(quiet cycles publish nothing). Needs an R2 API token scoped to the
`bccams-lite` bucket for the archiver's S3 uploads (mint in dashboard).

R2 bucket CORS (dashboard → bucket → CORS, or `wrangler r2 bucket cors`):

```json
[
  {
    "AllowedOrigins": ["https://<your-site-domain>", "http://localhost:8123"],
    "AllowedMethods": ["GET"],
    "MaxAgeSeconds": 3600
  }
]
```

## History analytics (DuckDB, operator-side)

The daemon appends one JSON object per line to `LOG_PATH`
(default `./data/archive-log.jsonl`), fsync'd every batch, plus a `type:"cycle"`
summary line — pull the file any time, even while the daemon runs, and query
it with the DuckDB CLI (no server, no import step):

```sh
duckdb -c "
SELECT camId, dir, count(*) AS updates
FROM read_json('archive-log.jsonl')
WHERE type = 'check' AND changed
GROUP BY 1, 2 ORDER BY 1, 2;"
```

Gaps between updates per camera (catches stale feeds):

```sql
SELECT camId, dir, CAST(ts AS TIMESTAMPTZ) AS update_at,
  date_diff('minute',
    lag(CAST(ts AS TIMESTAMPTZ)) OVER (PARTITION BY camId, dir ORDER BY ts),
    CAST(ts AS TIMESTAMPTZ)) AS mins_since_prev
FROM read_json('archive-log.jsonl')
WHERE type = 'check' AND changed
ORDER BY 1, 2, 3;
```

Fetch/upload errors:

```sql
SELECT ts, camId, dir, error
FROM read_json('archive-log.jsonl')
WHERE error IS NOT NULL
ORDER BY ts;
```

## Acceptance (all must pass)

- [ ] `bun scripts/build.mjs` exits 0; every URL in `data/cams.json` verified live.
- [ ] Burrard St & Davie St present with N/E/S/W stills (regression test for the bccams.ca gap).
- [ ] Bute St & Davie St present with N/E/S/W stills.
- [ ] `public/index.html` < 100KB; images lazy-load; page works from `python3 -m http.server -d public` with no console errors.
- [ ] Map renders pins; search filters the list; modal auto-refreshes.
- [ ] No backend process in the request path; no secrets in repo (`grep -ri secret public data scripts cmd` clean of values).

## Verify before handing back

```sh
bun scripts/build.mjs
go vet ./... && go build ./cmd/archive
ls -la public/ && wc -c public/index.html
python3 -m http.server -d public 8123
# open http://localhost:8123, click Burrard & Davie, confirm 4 stills load
```
