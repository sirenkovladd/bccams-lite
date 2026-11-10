# bccams-lite v1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build static, no-backend Vancouver traffic-cam viewer satisfying all PROMPT.md acceptance checks.

**Architecture:** `scripts/build.mjs` (bun) verifies every image URL live via HEAD/GET and writes `data/cams.json`; `public/` is vanilla JS + Leaflet CDN reading `cams.json` at runtime (fetch, no build step). No proxy, no keys, no server history.

**Tech Stack:** Bun (build script), vanilla JS + Leaflet 1.9.4 via unpkg CDN + OSM tiles, plain CSS. Static hosting (Cloudflare Pages / `python3 -m http.server`).

**Spec:** `PROMPT.md` in repo root (acceptance list is the gate). Seed: `data/seed.json`.

## Global Constraints

- Static only: no backend, no image proxy, no database, no framework build step.
- Every image URL in `data/cams.json` verified live with HEAD/GET in `scripts/build.mjs`; build fails (nonzero exit) on any unverified URL.
- Never invent image URLs — only URLs scraped from source pages or already in `data/seed.json`.
- Camera record shape: `{id, name, neighbourhood, page, lat, lng, images: {N,E,S,W}}` (`page` = source page URL; `lat`/`lng` added for Leaflet pins).
- `public/index.html` < 100KB; images `loading="lazy"`; page works from `python3 -m http.server -d public` with no console errors.
- Temp captures and probe scripts go in `$TMPDIR`, never the repo.
- No secrets in output (`grep -ri secret public data scripts` clean).

## Review Focus

- City site serves `cameraimages/*.jpg` as relative URLs — plan pins absolute `https://trafficcams.vancouver.ca/cameraimages/...` so hotlinking works from any host. Test: build output URLs all start with `https://`.
- Burrard & Pacific South still is `BurrardBridgeSouth.jpg`, not `BurrardPacificSouth.jpg` — a reasonable reader expects a symmetric name and "fixes" it. Test: Task 1 asserts exact S URL `.../BurrardBridgeSouth.jpg` returns HTTP 200 image/jpeg.
- City JPEGs update every ~10–15 min and browsers cache aggressively — a refresh that re-sets the same `src` shows a stale frame. Test: Task 3 cache-busts stills on refresh (`?t=Date.now()`) while keeping canonical URLs in `cams.json`.
- ~200 intersections on the index page tempt a full scrape; v1 scope is 4 West End cams so verification stays deterministic. Test: Task 1 asserts `cams.json` has exactly 4 entries (ids below).
- OSM tile + Leaflet CDN require network at runtime; offline file:// or blocked CDN must not break the list/modal. Test: Task 3 works with map absent (list, search, modal all function if `L` is undefined).

---

### Task 1: Build script + verified camera data

**Files:**
- Create: `scripts/build.mjs`
- Create (generated, committed): `data/cams.json`

**Interfaces:**
- Consumes: `data/seed.json` (`mustInclude[2]`, `nextToScrape`)
- Produces: `data/cams.json` — array of 4 objects `{id, name, neighbourhood, page, lat, lng, images:{N,E,S,W}}`; consumed by `public/app.js` via `fetch('cams.json')` (same dir). Exact ids: `burrard-davie`, `bute-davie`, `burrard-drake`, `burrard-pacific`.

- [ ] **Step 1: Write the camera table into `scripts/build.mjs` as data (no network yet)**

  Exact 4 records the script must emit (names/URLs verbatim):
  - `burrard-davie` | "Burrard St & Davie St" | West End | page `https://trafficcams.vancouver.ca/burrardDavie.htm` | lat 49.2791 lng -123.1292 | N `.../BurrardDavieNorth.jpg`, E `.../BurrardDavieEast.jpg`, S `.../BurrardDavieSouth.jpg`, W `.../BurrardDavieWest.jpg`
  - `bute-davie` | "Bute St & Davie St" | West End | page `https://trafficcams.vancouver.ca/buteDavie.htm` | lat 49.2818 lng -123.1334 | N `.../ButeNorthDavie.jpg`, E `.../DavieEastBute.jpg`, S `.../ButeSouthDavie.jpg`, W `.../DavieWestBute.jpg`
  - `burrard-drake` | "Burrard St & Drake St" | West End | page `https://trafficcams.vancouver.ca/burrardDrake.htm` | lat 49.2809 lng -123.1289 | N `.../BurrardDrakeNorth.jpg`, E `.../BurrardDrakeEast.jpg`, S `.../BurrardDrakeSouth.jpg`, W `.../BurrardDrakeWest.jpg`
  - `burrard-pacific` | "Burrard St & Pacific Ave" | West End | page `https://trafficcams.vancouver.ca/BurrardPacific4.htm` | lat 49.2775 lng -123.1285 | N `.../BurrardPacificNorth.jpg`, E `.../BurrardPacificEast.jpg`, S `.../BurrardBridgeSouth.jpg` (note asymmetric name — do not "fix"), W `.../BurrardPacificWest.jpg`
  - All image URLs prefixed `https://trafficcams.vancouver.ca/cameraimages/`. Cross-check seed: first two records' image filenames must match `data/seed.json` exactly.

- [ ] **Step 2: Implement verification + write in `scripts/build.mjs`**

  Signature/behavior: `bun scripts/build.mjs` → for each of 16 URLs: `fetch(url, {method:'HEAD'})`, fall back to GET on 405; assert status 200 and `content-type` starts with `image/`; on any failure print URL + status and `process.exit(1)`; on success write pretty-printed `data/cams.json` (the 4 records above) and print `wrote data/cams.json (4 cams, 16 images verified)`.

- [ ] **Step 3: Run the build**

  Run: `bun scripts/build.mjs`
  Expected: exit 0, prints verification line; `data/cams.json` exists with 4 entries each having N/E/S/W https URLs.

- [ ] **Step 4: Commit**

  ```bash
  git add scripts/build.mjs data/cams.json
  git commit -m "feat: add verified build script and 4-cam dataset"
  ```

### Task 2: Static shell (index.html + style.css)

**Files:**
- Create: `public/index.html`
- Create: `public/style.css`

**Interfaces:**
- Consumes: `data/cams.json` shape from Task 1 (for element ids/classes `app.js` will use — defined here).
- Produces: DOM contract for Task 3: `#map`, `#search` (input), `#hood-filter` (select), `#cam-list`, `#modal` (with `#modal-title`, `#modal-stills`, `#frame-strip`, `#modal-close`), `#count`. Leaflet 1.9.4 CSS+JS via unpkg CDN with `defer`.

- [ ] **Step 1: Write `public/index.html` skeleton**

  Contains (verbatim ids): header with title "bccams-lite" + link-back note; controls (`#search` placeholder "Search intersections…", `#hood-filter` with "All neighbourhoods" + "West End"); main split `#map` + `#cam-list`; `#count`; modal `#modal[hidden]` > `#modal-title`, `#modal-stills` (4 `figure > img[loading=lazy] + figcaption` slots for N/E/S/W + timestamp `<time>` each), `#frame-strip`, `#modal-close`. Leaflet CSS in head, Leaflet JS + `app.js` deferred at body end. No inline image URLs. Must stay < 100KB (target < 10KB).

- [ ] **Step 2: Write `public/style.css`**

  Mobile-first: header bar, controls row, two-column (list 320px + map) collapsing to single column under 800px; modal overlay centered, stills grid 2×2 collapsing to 1 col; frame strip horizontal thumbs. No framework, no external fonts.

- [ ] **Step 3: Verify size and no-console-error load**

  Run: `wc -c public/index.html && python3 -m http.server -d public 8123` then open `http://localhost:8123` and check console empty (map may be empty — Task 3 fills it).
  Expected: `public/index.html` < 102400 bytes; no console errors.

- [ ] **Step 4: Commit**

  ```bash
  git add public/index.html public/style.css
  git commit -m "feat: add static shell with map, list, and modal"
  ```

### Task 3: Frontend app (app.js)

**Files:**
- Create: `public/app.js`

**Interfaces:**
- Consumes: DOM contract from Task 2; `fetch('cams.json')` (same-dir; works over http, degrades over file:// with message).
- Produces: rendered pins + list; behaviors below. No exports (script tag).

- [ ] **Step 1: Write list/map/search/filter rendering**

  On load: `fetch('cams.json')` → render `#cam-list` buttons (name + neighbourhood), Leaflet pins at `(lat,lng)` with name popup (guard: `if (window.L)` so page works without CDN); `#search` filters by name substring case-insensitive; `#hood-filter` filters by neighbourhood; `#count` shows "N cams". Clicking list item or pin opens modal.

- [ ] **Step 2: Write modal + auto-refresh + frame history**

  Modal shows 4 stills N/E/S/W with `loading="lazy"`, each with `<time>` "updated HH:MM:SS" timestamp set on load; `setInterval` 60s re-fetches stills with cache-buster (`src = canonical + '?t=' + Date.now()`) and updates timestamps; keep in-memory per-cam ring buffer of last 6 fetch timestamps' URLs rendered as `#frame-strip` thumbnails (session-only, plus `localStorage` key `bccams-lite:lastSeen:<id>` timestamp — no image bytes stored). Close on `#modal-close`, overlay click, Escape; clear interval on close.

- [ ] **Step 3: Verify in browser**

  Run: `python3 -m http.server -d public 8123`, open `http://localhost:8123`, click "Burrard St & Davie St" → 4 stills load; search "bute" filters to 1; console has no errors.
  Expected: all pass; network tab shows `cams.json` + 4 JPEGs with `loading=lazy` on below-fold images.

- [ ] **Step 4: Commit**

  ```bash
  git add public/app.js
  git commit -m "feat: add viewer app with search, pins, and auto-refresh modal"
  ```

### Task 4: Docs + acceptance gate

**Files:**
- Modify: `README.md` (deploy section)

**Interfaces:**
- Consumes: all prior tasks.
- Produces: deploy docs + full gate result.

- [ ] **Step 1: Update `README.md` deploy section**

  Exact commands documented: `bun scripts/build.mjs`; `npx wrangler pages deploy public --project-name bccams-lite`; 512MB-box one-liner `python3 -m http.server -d public 80` (primary) with `caddy file-server --root public --listen :80` alternative. Note images hotlink `trafficcams.vancouver.ca` with link-back to source pages; no secrets/keys.

- [ ] **Step 2: Run the full acceptance gate**

  Run:
  ```bash
  bun scripts/build.mjs
  ls -la public/ && wc -c public/index.html
  grep -ri secret public data scripts || echo "no secrets"
  python3 -m http.server -d public 8123
  ```
  Expected: build exit 0; `index.html` < 102400 bytes; grep clean; Burrard & Davie and Bute & Davie present in `cams.json` with N/E/S/W; manual click confirms 4 stills + 60s refresh + frame strip.

- [ ] **Step 3: Commit**

  ```bash
  git add README.md
  git commit -m "docs: document static deploy and acceptance gate"
  ```
