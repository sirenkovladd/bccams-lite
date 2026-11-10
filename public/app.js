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
  const q = document.getElementById("search").value.trim().toLowerCase();
  return cams.filter(
    (c) =>
      (!activeRegion || c.neighbourhood === activeRegion) &&
      (!q || c.name.toLowerCase().includes(q) || c.neighbourhood.toLowerCase().includes(q)),
  );
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
    if (e.key === "Escape" && !document.getElementById("modal").hidden) closeModal();
  });
  document.getElementById("frame-strip").addEventListener("click", (e) => {
    const img = e.target.closest("img[data-dir]");
    if (!img || !current) return;
    const slot = document.querySelector(`#modal-stills img[data-dir="${img.dataset.dir}"]`);
    if (!slot) return;
    slot.src = img.src;
    slot.classList.remove("on");
    slot.addEventListener("load", () => slot.classList.add("on"), { once: true });
    const time = document.querySelector(`#modal-stills time[data-dir="${img.dataset.dir}"]`);
    if (time && img.dataset.ts && +img.dataset.ts > 0) {
      time.textContent = new Date(+img.dataset.ts).toLocaleTimeString();
    }
    document
      .querySelectorAll("#frame-strip img.sel")
      .forEach((el) => el.classList.remove("sel"));
    img.classList.add("sel");
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

const STRIP_MAX = 24; // timeline thumbs cap: enough history, no spam
// One timeline renderer for live session frames and archived shard frames.
// entries: [{dir, url, ts}]. Drops whatever is showing in the stills above
// (full-URL compare: live frames differ only by ?t= cache-buster),
// newest first, grouped by hour.
function setStrip(entries) {
  const strip = document.getElementById("frame-strip");
  strip.innerHTML = "";
  if (!current) {
    updateStripHead();
    return;
  }
  const stillSrc = {};
  for (const dir of Object.keys(current.cam.images)) {
    const slot = document.querySelector(`#modal-stills img[data-dir="${dir}"]`);
    if (slot && slot.src) stillSrc[dir] = slot.src;
  }
  const fresh = entries
    .filter((e) => e.url !== stillSrc[e.dir])
    .sort((a, b) => b.ts - a.ts)
    .slice(0, STRIP_MAX);
  let lastHour = null;
  let row = null;
  for (const e of fresh) {
    const hour = new Date(e.ts).toTimeString().slice(0, 2) + ":00";
    if (hour !== lastHour) {
      const grp = document.createElement("div");
      grp.className = "fr-group";
      const lab = document.createElement("span");
      lab.className = "fr-hour";
      lab.textContent = hour;
      row = document.createElement("div");
      row.className = "fr-thumbs";
      grp.append(lab, row);
      strip.append(grp);
      lastHour = hour;
    }
    const img = document.createElement("img");
    img.loading = "lazy";
    img.decoding = "async";
    img.dataset.dir = e.dir;
    img.dataset.ts = e.ts;
    img.alt = `${current.cam.name} ${e.dir} frame at ${new Date(e.ts).toLocaleTimeString()}`;
    img.src = e.url;
    row.append(img);
  }
  updateStripHead();
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
