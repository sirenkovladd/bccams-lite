"use strict";
/* bccams-lite full-city viewer: bccams-style region grid + map tab + search.
 * Region order comes from cams.json itself (the build sorts by regions.json
 * order, so first-seen order IS the canonical order — one mechanism, no
 * extra fetch). Regions after the first render collapsed (native <details>).
 * R2 history: per-cam shard <base>/history/<cam-id>/manifest.json; a top-level
 * manifest with a cams map is also accepted (tolerant reader). */
const DIR_ORDER = ["N", "E", "S", "W"];
const DIR_LABEL = { N: "North", E: "East", S: "South", W: "West" };
const SEARCH_CAP = 200;
const REGION_FEEDS = 12; // per-region teaser cap in the unfiltered All view

let cams = [];
let byRegion = []; // [{region, cams:[...]}] in canonical order
let historyBase = "";
let map = null;
let mapMarkers = [];
let mapFitDone = false;
let activeRegion = ""; // chip filter ("" = all)
let current = null; // {cam, timer, frames:{DIR:[url,...]}}

function slug(s) {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
}

async function load() {
  const [camsRes, histRes] = await Promise.all([
    fetch("cams.json"),
    fetch("history.json").catch(() => null),
  ]);
  cams = await camsRes.json();
  if (histRes && histRes.ok) {
    try {
      const h = await histRes.json();
      if (h && typeof h.base === "string" && h.base) historyBase = h.base.replace(/\/$/, "");
    } catch {
      /* ignore malformed pointer */
    }
  }
  groupRegions();
  renderStats();
  renderChips();
  applyFilter();
  wireControls();
  // NOTE: no initMap() here. The map is created lazily on first Map-tab show:
  // L.map() + fitBounds on the hidden pane poisons Leaflet's cached size (0x0)
  // and the first fit sticks as a world view (invalidateSize is a no-op
  // while the map has no view yet, so the stale size never self-heals).
}

function groupRegions() {
  const seen = new Map();
  for (const cam of cams) {
    if (!seen.has(cam.neighbourhood)) seen.set(cam.neighbourhood, []);
    seen.get(cam.neighbourhood).push(cam);
  }
  byRegion = [...seen.entries()].map(([region, list]) => ({ region, cams: list }));
}

function visibleCams() {
  const words = document.getElementById("search").value.trim().toLowerCase().split(/\s+/).filter(Boolean);
  return cams.filter((c) => {
    if (activeRegion && c.neighbourhood !== activeRegion) return false;
    if (!words.length) return true;
    // Every word must appear somewhere in name + neighbourhood, in any order:
    // "davie burrard" and "burrard davie" both find Burrard St and Davie St.
    const hay = `${c.name} ${c.neighbourhood}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
}

function renderStats() {
  const dirs = cams.reduce((n, c) => n + Object.keys(c.images).length, 0);
  document.getElementById("stats").textContent =
    `${byRegion.length} regions · ${cams.length} cams · ${dirs} feeds · 24/7`;
}

function renderChips() {
  const box = document.getElementById("region-chips");
  box.innerHTML = "";
  const all = document.createElement("button");
  all.type = "button";
  all.textContent = "All";
  all.className = activeRegion ? "" : "on";
  all.addEventListener("click", () => setRegion(""));
  box.append(all);
  for (const { region, cams: list } of byRegion) {
    const b = document.createElement("button");
    b.type = "button";
    b.dataset.region = region;
    b.className = activeRegion === region ? "on" : "";
    const name = document.createElement("span");
    name.textContent = region;
    const n = document.createElement("span");
    n.className = "n";
    n.textContent = ` ${list.length}`;
    b.append(name, n);
    b.addEventListener("click", () => setRegion(activeRegion === region ? "" : region));
    box.append(b);
  }
}

function setRegion(region) {
  activeRegion = region;
  renderChips();
  applyFilter();
  if (region) {
    const sec = document.getElementById("region-" + slug(region));
    if (sec) {
      sec.open = true;
      sec.scrollIntoView({ block: "start" });
    }
  }
}

function regionSections(list) {
  // Group a cam list back into canonical region order.
  const inList = new Set(list);
  return byRegion
    .map(({ region, cams: all }) => ({ region, cams: all.filter((c) => inList.has(c)) }))
    .filter((g) => g.cams.length > 0);
}

// Short card label: "Beatty St and Dunsmuir St" -> "Beatty–Dunsmuir".
// Road suffixes go; directional prefixes (W Pender vs E Pender) stay.
const ROAD_SUFFIX = /\s+(St|Ave|Av|Blvd|Dr|Rd|Way|Cres)\.?$/i;
function shortName(name) {
  return name
    .split(/\s+and\s+|\s*&\s*|\s*@\s*/i)
    .map((p) => p.trim().replace(ROAD_SUFFIX, ""))
    .filter(Boolean)
    .join("–");
}

function cardFigure(cam, dir) {
  const label = DIR_LABEL[dir] || dir;
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "card";
  btn.setAttribute("aria-label", `${cam.name} — ${label}. Open camera.`);
  const img = document.createElement("img");
  img.loading = "lazy";
  img.decoding = "async";
  img.alt = "";
  img.addEventListener("load", () => img.classList.add("on"), { once: true });
  img.src = cam.images[dir];
  const ol = document.createElement("span");
  ol.className = "card-ol";
  ol.setAttribute("aria-hidden", "true");
  const nm = document.createElement("span");
  nm.className = "card-name";
  nm.textContent = shortName(cam.name);
  const badge = document.createElement("span");
  badge.className = "card-dir";
  badge.textContent = dir;
  const live = document.createElement("span");
  live.className = "card-live";
  ol.append(nm, badge, live);
  btn.append(img, ol);
  btn.addEventListener("click", () => openModal(cam));
  return btn;
}

// Render first 2 regions synchronously (fast first paint); the rest when the
// browser is idle (requestIdleCallback, setTimeout fallback) — one mechanism.
// A render token drops stale idle callbacks when a new filter starts.
// Total feeds rendered per cycle are capped at SEARCH_CAP, with a note.
let renderToken = 0;
let renderedFeeds = 0;
let restScheduled = false;

function applyFilter() {
  renderToken++;
  restScheduled = false;
  renderedFeeds = 0;
  renderPass(renderToken, 0, 2);
  updateNote();
  drawPins();
  scheduleRest(renderToken);
}

function scheduleRest(token) {
  const total = regionSections(visibleCams()).length;
  if (restScheduled || total <= 2) return;
  restScheduled = true;
  const run = () => {
    restScheduled = false;
    if (token !== renderToken) return;
    renderPass(token, 2, Infinity);
    updateNote();
  };
  if (typeof requestIdleCallback === "function") requestIdleCallback(run, { timeout: 2000 });
  else setTimeout(run, 50);
}

function renderPass(token, from, to) {
  if (token !== renderToken) return;
  const box = document.getElementById("regions");
  if (from === 0) box.innerHTML = "";
  const query = document.getElementById("search").value.trim();
  // Filtered (chip or search): one flat grid, no section headers — the filter
  // already says what you're looking at. Unfiltered: sections capped per
  // region so every region stays browsable (a global fill starves Downtown).
  const flat = Boolean(activeRegion || query);
  if (flat && from > 0) return; // filtered view renders in a single pass
  const cap = query ? SEARCH_CAP : Infinity;
  const groups = regionSections(visibleCams());
  let grid = null;
  if (flat) {
    grid = document.createElement("div");
    grid.className = "cards";
    box.append(grid);
  }
  groups.slice(from, flat ? undefined : to).forEach((g, gi) => {
    if (!flat) {
      const det = document.createElement("details");
      det.className = "region";
      det.id = "region-" + slug(g.region);
      det.open = from + gi === 0;
      const sum = document.createElement("summary");
      const h = document.createElement("h2");
      h.textContent = g.region;
      const cnt = document.createElement("span");
      cnt.className = "cnt";
      cnt.textContent = `${g.cams.length} cams`;
      h.append(cnt);
      sum.append(h);
      grid = document.createElement("div");
      grid.className = "cards";
      det.append(sum, grid);
      box.append(det);
    }
    let shown = 0;
    const full = () => renderedFeeds >= cap || (!flat && shown >= REGION_FEEDS);
    for (const cam of g.cams) {
      for (const dir of DIR_ORDER.filter((d) => cam.images[d])) {
        if (full()) break;
        grid.append(cardFigure(cam, dir));
        renderedFeeds++;
        shown++;
      }
      if (full()) break;
    }
  });
}

function updateNote() {
  const box = document.getElementById("regions");
  let note = document.getElementById("grid-note");
  if (!note) {
    note = document.createElement("p");
    note.id = "grid-note";
    box.after(note);
  }
  const total = regionSections(visibleCams()).reduce(
    (n, g) => n + g.cams.reduce((m, c) => m + Object.keys(c.images).length, 0),
    0,
  );
  note.textContent =
    renderedFeeds >= total
      ? `${total} feeds shown`
      : `Showing ${renderedFeeds} of ${total} feeds — refine search for more`;
}

function initMap() {
  if (!window.L || typeof window.L.map !== "function") return;
  map = window.L.map("map");
  window.L.tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", {
    maxZoom: 19,
    attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OSM</a>',
  }).addTo(map);
  drawPins();
}

function drawPins() {
  if (!map) return;
  for (const m of mapMarkers) m.remove();
  mapMarkers = [];
  const list = visibleCams().filter((c) => c.lat && c.lng);
  for (const cam of list) {
    const mk = window.L.marker([cam.lat, cam.lng]).addTo(map).bindPopup(cam.name);
    mk.on("click", () => openModal(cam));
    mapMarkers.push(mk);
  }
  if (list.length && !mapFitDone) {
    map.fitBounds(window.L.latLngBounds(list.map((c) => [c.lat, c.lng])).pad(0.1));
    mapFitDone = true;
  }
}

function wireControls() {
  document.getElementById("search").addEventListener("input", applyFilter);
  document.getElementById("tab-grid").addEventListener("click", () => showTab("grid"));
  document.getElementById("tab-map").addEventListener("click", () => showTab("map"));
  document.getElementById("modal-close").addEventListener("click", () => closeModal());
  document.getElementById("modal").addEventListener("click", (e) => {
    if (e.target.id === "modal") closeModal();
  });
  document.addEventListener("keydown", (e) => {
    if (document.getElementById("modal").hidden || !current || !current.tl) return;
    if (e.key === "Escape") closeModal();
    else if (e.key === "ArrowLeft") tlStep(-1);
    else if (e.key === "ArrowRight") tlStep(1);
  });

}

function showTab(which) {
  document.getElementById("tab-grid").setAttribute("aria-selected", String(which === "grid"));
  document.getElementById("tab-map").setAttribute("aria-selected", String(which === "map"));
  document.getElementById("view-grid").hidden = which !== "grid";
  document.getElementById("view-map").hidden = which !== "map";
  if (which === "map") {
    if (!map) initMap(); // pane is visible now: real size, fit lands on Vancouver
    else {
      map.invalidateSize();
      drawPins();
    }
  }
}

function stillUrl(cam, dir, cacheBust) {
  return cam.images[dir] + (cacheBust ? "?t=" + Date.now() : "");
}

function fmtTime(d) {
  return d.toTimeString().slice(0, 8);
}

function openModal(cam) {
  closeModal();
  current = { cam, timer: null, frames: {} };
  for (const d of Object.keys(cam.images)) current.frames[d] = [];
  document.getElementById("modal-title").textContent = cam.name;
  document.getElementById("modal-source").href = cam.page;
  document.getElementById("modal-maps").href =
    `https://www.google.com/maps/search/?api=1&query=${cam.lat},${cam.lng}`;
  const stills = document.getElementById("modal-stills");
  stills.innerHTML = "";
  for (const dir of DIR_ORDER.filter((d) => cam.images[d])) {
    const fig = document.createElement("figure");
    const img = document.createElement("img");
    img.dataset.dir = dir;
    img.loading = "lazy";
    img.alt = `${cam.name} — ${DIR_LABEL[dir]}`;
    img.addEventListener("load", () => img.classList.add("on"), { once: true });
    img.src = stillUrl(cam, dir, true);
    const cap = document.createElement("figcaption");
    const label = document.createElement("span");
    label.textContent = DIR_LABEL[dir];
    const time = document.createElement("time");
    time.dataset.dir = dir;
    time.textContent = "updated " + fmtTime(new Date());
    cap.append(label, time);
    fig.append(img, cap);
    stills.append(fig);
    pushFrame(cam.id, dir, img.src);
  }
  try {
    localStorage.setItem("bccams-lite:lastSeen:" + cam.id, new Date().toISOString());
  } catch {
    /* private mode etc. */
  }
  renderStrip();
  enhanceFromHistory(cam);
  current.timer = setInterval(() => refreshStills(), 60000);
  document.getElementById("modal").hidden = false;
}

function pushFrame(camId, dir, url) {
  if (!current || current.cam.id !== camId) return;
  const buf = current.frames[dir] || (current.frames[dir] = []);
  buf.unshift({ url, ts: Date.now() });
  if (buf.length > 6) buf.length = 6;
}

function refreshStills() {
  if (!current) return;
  const now = fmtTime(new Date());
  for (const dir of Object.keys(current.cam.images)) {
    const img = document.querySelector(`#modal-stills img[data-dir="${dir}"]`);
    const time = document.querySelector(`#modal-stills time[data-dir="${dir}"]`);
    if (!img) continue;
    img.src = stillUrl(current.cam, dir, true);
    if (time) time.textContent = "updated " + now;
    pushFrame(current.cam.id, dir, img.src);
  }
  try {
    localStorage.setItem("bccams-lite:lastSeen:" + current.cam.id, new Date().toISOString());
  } catch {
    /* ignore */
  }
  renderStrip();
}

function updateStripHead() {
  const strip = document.getElementById("frame-strip");
  document.getElementById("prev-frames-h").hidden = !strip.hasChildNodes();
}

const STRIP_MAX = 150; // dots are cheap; the old thumb strip was capped instead
const GROUP_WINDOW = 120000; // frames within 2 min form one moment (one poll cycle)
const TL_MIN_SPAN = 120000; // closest zoom: 2 minutes

// One timeline renderer for live session frames and archived shard frames.
// entries: [{dir, url, ts}]. Current stills are excluded (no duplicate of what
// is showing above); the rest cluster into moments by capture time. Arrows
// and dots jump whole moments: every direction's still updates at once.
function groupByTime(entries) {
  const sorted = entries.slice().sort((a, b) => a.ts - b.ts);
  const groups = [];
  for (const e of sorted) {
    const last = groups[groups.length - 1];
    if (last && e.ts - last.end <= GROUP_WINDOW) {
      last.frames.push(e);
      if (e.ts > last.end) last.end = e.ts;
    } else {
      groups.push({ start: e.ts, end: e.ts, frames: [e] });
    }
  }
  return groups;
}

function tlFullRange() {
  const T = current.tl;
  const lo = T.groups[0].start, hi = T.groups[T.groups.length - 1].end;
  return lo === hi ? { start: lo - 300000, end: hi + 300000 } : { start: lo, end: hi };
}

function renderTimeline() {
  const strip = document.getElementById("frame-strip");
  strip.innerHTML = "";
  const T = current.tl;
  if (!T.groups.length) {
    updateStripHead();
    return;
  }
  if (!T.view) T.view = tlFullRange();
  const { start, end } = T.view;
  const span = Math.max(end - start, 1);
  const nav = document.createElement("div");
  nav.className = "tl-nav";
  const prev = document.createElement("button");
  prev.type = "button";
  prev.className = "tl-arrow";
  prev.setAttribute("aria-label", "Previous moment");
  prev.textContent = "\u2039";
  const next = document.createElement("button");
  next.type = "button";
  next.className = "tl-arrow";
  next.setAttribute("aria-label", "Next moment");
  next.textContent = "\u203a";
  const bar = document.createElement("div");
  bar.className = "tl-bar";
  bar.setAttribute("role", "slider");
  bar.setAttribute("aria-label", "Frame timeline");
  bar.setAttribute("tabindex", "0");
  const track = document.createElement("div");
  track.className = "tl-track";
  bar.append(track);
  const readout = document.createElement("span");
  readout.className = "tl-readout";
  const zin = document.createElement("button");
  zin.type = "button";
  zin.className = "tl-arrow";
  zin.setAttribute("aria-label", "Zoom timeline in");
  zin.textContent = "+";
  const zout = document.createElement("button");
  zout.type = "button";
  zout.className = "tl-arrow";
  zout.setAttribute("aria-label", "Zoom timeline out");
  zout.textContent = "\u2212";
  nav.append(prev, bar, next, readout, zin, zout);
  strip.append(nav);

  const EDGE = 12; // px inset: edge dots stay fully inside the bar and clickable
  const frac = (ts) => Math.min(1, Math.max(0, (ts - start) / span));
  T.groups.forEach((g, i) => {
    const ts = (g.start + g.end) / 2;
    const dot = document.createElement("button");
    dot.type = "button";
    dot.className = "tl-dot" + (i === T.sel ? " sel" : "");
    dot.dataset.i = i;
    dot.title = `${new Date(g.start).toLocaleTimeString()} · ${g.frames.length} frames`;
    dot.setAttribute("aria-label", `Moment ${i + 1} of ${T.groups.length} at ${new Date(g.start).toLocaleString()}`);
    dot.style.left = `calc(${EDGE}px + ${frac(ts)} * (100% - ${2 * EDGE}px))`;
    dot.addEventListener("click", () => tlSelect(i));
    bar.append(dot);
  });
  prev.addEventListener("click", () => tlStep(-1));
  next.addEventListener("click", () => tlStep(1));
  zin.addEventListener("click", () => tlZoom(0.5));
  zout.addEventListener("click", () => tlZoom(2));
  bar.addEventListener("click", (e) => {
    if (e.target !== bar && e.target !== track) return;
    const r = bar.getBoundingClientRect();
    const f = Math.min(1, Math.max(0, (e.clientX - r.left - EDGE) / Math.max(r.width - 2 * EDGE, 1)));
    const ts = start + f * span;
    let best = 0, bd = Infinity;
    T.groups.forEach((g, i) => {
      const c = (g.start + g.end) / 2;
      const d = Math.abs(c - ts);
      if (d < bd) { bd = d; best = i; }
    });
    tlSelect(best);
  });
  tlReadout();
  updateStripHead();
}

function tlReadout() {
  const T = current && current.tl;
  if (!T) return;
  const el = document.querySelector("#frame-strip .tl-readout");
  if (!el) return;
  if (T.sel >= 0) {
    const g = T.groups[T.sel];
    el.textContent = `${new Date(g.start).toLocaleTimeString()} · ${T.sel + 1}/${T.groups.length}`;
  } else {
    el.textContent = `${T.groups.length} moments`;
  }
}

function tlGroupTs() {
  const g = current.tl.groups[current.tl.sel];
  return (g.start + g.end) / 2;
}

function tlEnsureVisible() {
  const T = current.tl;
  const ts = tlGroupTs();
  if (ts < T.view.start || ts > T.view.end) {
    const span = Math.max(T.view.end - T.view.start, TL_MIN_SPAN);
    T.view = { start: ts - span / 2, end: ts + span / 2 };
  }
}

function tlSelect(i) {
  const T = current.tl;
  T.sel = Math.max(0, Math.min(i, T.groups.length - 1));
  tlEnsureVisible();
  for (const f of T.groups[T.sel].frames) {
    const slot = document.querySelector(`#modal-stills img[data-dir="${f.dir}"]`);
    if (!slot || slot.src === f.url) continue;
    slot.src = f.url;
    slot.classList.remove("on");
    slot.addEventListener("load", () => slot.classList.add("on"), { once: true });
    const time = document.querySelector(`#modal-stills time[data-dir="${f.dir}"]`);
    if (time) time.textContent = new Date(f.ts).toLocaleTimeString();
  }
  renderTimeline();
}

function tlZoom(factor) {
  const T = current.tl;
  const full = tlFullRange();
  const anchor = T.sel >= 0 ? tlGroupTs() : (T.view.start + T.view.end) / 2;
  let span = (T.view.end - T.view.start) * factor;
  span = Math.min(span, full.end - full.start);
  if (span <= TL_MIN_SPAN && factor < 1) return;
  span = Math.max(span, TL_MIN_SPAN);
  let start = anchor - span / 2, end = anchor + span / 2;
  if (start < full.start) { end += full.start - start; start = full.start; }
  if (end > full.end) { start -= end - full.end; end = full.end; }
  if (start < full.start) { start = full.start; end = full.end; }
  T.view = { start, end };
  renderTimeline();
}

function tlStep(d) {
  const T = current.tl;
  if (!T.groups.length) return;
  if (T.sel < 0) return tlSelect(d > 0 ? T.groups.length - 1 : 0);
  if (T.sel + d < 0 || T.sel + d >= T.groups.length) return;
  tlSelect(T.sel + d);
}

function setStrip(entries) {
  if (current) {
    const stillSrc = {};
    for (const dir of Object.keys(current.cam.images)) {
      const slot = document.querySelector(`#modal-stills img[data-dir="${dir}"]`);
      if (slot && slot.src) stillSrc[dir] = slot.src;
    }
    current.tl = {
      groups: groupByTime(
        entries.filter((e) => e.ts > 0 && e.url !== stillSrc[e.dir]).sort((a, b) => b.ts - a.ts).slice(0, STRIP_MAX),
      ),
      view: null,
      sel: -1,
    };
    renderTimeline();
  } else {
    document.getElementById("frame-strip").innerHTML = "";
    updateStripHead();
  }
}

function renderStrip() {
  if (!current) {
    setStrip([]);
    return;
  }
  const entries = [];
  for (const dir of DIR_ORDER.filter((d) => current.frames[d])) {
    for (const f of current.frames[dir]) entries.push({ dir, url: f.url, ts: f.ts });
  }
  setStrip(entries);
}

async function enhanceFromHistory(cam) {
  if (!historyBase || !current || current.cam.id !== cam.id) return;
  let manifest;
  try {
    const res = await fetch(`${historyBase}/history/${cam.id}/manifest.json`, { cache: "no-store" });
    if (!res.ok) throw new Error("HTTP " + res.status);
    manifest = await res.json();
  } catch (err) {
    console.warn("history unavailable, using live frames:", err.message);
    return;
  }
  // Tolerant reader: per-cam shard (dir map at top level) or top-level cams map.
  const entry = manifest && manifest.cams && manifest.cams[cam.id] ? manifest.cams[cam.id] : manifest;
  if (!entry || !current || current.cam.id !== cam.id) return;
  const entries = [];
  for (const dir of DIR_ORDER.filter((d) => cam.images[d])) {
    for (const rel of entry[dir] || []) {
      // Shard keys look like history/<cam>/<dir>/2026-11-10T12-00-00.jpg
      // (UTC, optional "local-" prefix); hour label only needs the hour.
      const m = /(\d{4}-\d{2}-\d{2}T\d{2})-\d{2}-\d{2}\.jpg$/.exec(rel);
      const ts = m ? Date.parse(m[1] + ":00:00Z") : 0;
      entries.push({ dir, url: historyBase + "/" + rel, ts });
    }
  }
  setStrip(entries);
}

function closeModal() {
  if (current && current.timer) clearInterval(current.timer);
  current = null;
  document.getElementById("modal").hidden = true;
}

if (typeof document !== "undefined" && document.getElementById("regions")) {
  load().catch((err) => {
    document.getElementById("stats").textContent = "failed to load cams.json";
    console.error(err);
  });
}
