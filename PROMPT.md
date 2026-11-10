# PROMPT.md — build bccams-lite v1 (paste this to the implementing agent)

## Goal

A static, no-backend Vancouver traffic-cam viewer deployable to Cloudflare Pages (free) or any 512MB box serving static files. It must fix the known bccams.ca gap and load fast.

## Non-goals

- No backend, no image proxy, no database, no framework build step.
- No server-side history. History is client-side only (in-memory frame cache + localStorage timestamps).

## Steps

1. Read `README.md`, `AGENTS.md`, `data/seed.json`.
2. Expand the camera list: scrape `https://trafficcams.vancouver.ca` (start with West End: Burrard & Davie, Bute & Davie, Burrard & Drake, Burrard & Pacific) and optionally DriveBC highway cams. Group by neighbourhood/region.
3. Write `scripts/build.mjs` (run with `bun`): reads seed + scraped additions, HEAD/GET-verifies every image URL, writes `data/cams.json` (`{id, name, neighbourhood, page, images: {N,E,S,W}}`), fails on any unverified URL.
4. Write `public/index.html`, `public/app.js`, `public/style.css` (vanilla JS, Leaflet via CDN for the map):
   - Map with pins + filterable list + text search.
   - Click a cam: modal with 4 direction stills, auto-refresh every 60s, timestamp labels.
   - Previous-frames strip: keep last 6 fetched frames per cam in memory this session.
   - `loading="lazy"` on all images, paginate/group so initial `index.html` stays under 100KB.
5. Document deploy in `README.md`: Cloudflare Pages command + one-liner static server for a 512MB box.

## Acceptance (all must pass)

- [ ] `bun scripts/build.mjs` exits 0; every URL in `data/cams.json` verified live.
- [ ] Burrard St & Davie St present with N/E/S/W stills (regression test for the bccams.ca gap).
- [ ] Bute St & Davie St present with N/E/S/W stills.
- [ ] `public/index.html` < 100KB; images lazy-load; page works from `python3 -m http.server -d public` with no console errors.
- [ ] Map renders pins; search filters the list; modal auto-refreshes.
- [ ] No backend process required; no secrets in repo (`grep -ri secret public data scripts` clean).

## Verify before handing back

```sh
bun scripts/build.mjs
ls -la public/ && wc -c public/index.html
python3 -m http.server -d public 8123
# open http://localhost:8123, click Burrard & Davie, confirm 4 stills load
```
