"use strict";
/* bccams-lite viewer: static list + Leaflet pins + modal with live refresh.
 * History: R2 manifest when history.json points at a base, else in-memory strip. */
const DIRS = ["N", "E", "S", "W"];

let cams = [];
let historyBase = "";
let historyManifest = null;
let map = null;
let markers = [];
let current = null; // { cam, timer, frames: {DIR: [url,...]} }

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
  renderList(cams);
  initMap();
  wireControls();
}

function matches(cam, q, hood) {
  const name = cam.name.toLowerCase();
  return (!q || name.includes(q)) && (!hood || cam.neighbourhood === hood);
}

function applyFilter() {
  const q = document.getElementById("search").value.trim().toLowerCase();
  const hood = document.getElementById("hood-filter").value;
  const shown = cams.filter((c) => matches(c, q, hood));
  renderList(shown);
  drawPins(shown);
}

function renderList(shown) {
  const ul = document.getElementById("cam-list");
  ul.innerHTML = "";
  for (const cam of shown) {
    const li = document.createElement("li");
    const b = document.createElement("button");
    b.type = "button";
    b.innerHTML = "";
    const strong = document.createElement("strong");
    strong.textContent = cam.name;
    const small = document.createElement("small");
    small.textContent = " " + cam.neighbourhood;
    b.append(strong, small);
    b.addEventListener("click", () => openModal(cam));
    li.append(b);
    ul.append(li);
  }
  document.getElementById("count").textContent = `${shown.length} cam${shown.length === 1 ? "" : "s"}`;
}

function initMap() {
  if (!window.L || typeof window.L.map !== "function") return; // CDN blocked: list still works
  map = window.L.map("map").setView([49.28, -123.13], 13);
  window.L.tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", {
    maxZoom: 19,
    attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OSM</a>',
  }).addTo(map);
  drawPins(cams);
}

function drawPins(shown) {
  if (!map) return;
  for (const m of markers) m.remove();
  markers = shown.map((cam) => {
    const mk = window.L.marker([cam.lat, cam.lng]).addTo(map).bindPopup(cam.name);
    mk.on("click", () => openModal(cam));
    return mk;
  });
}

function wireControls() {
  document.getElementById("search").addEventListener("input", applyFilter);
  document.getElementById("hood-filter").addEventListener("change", applyFilter);
  document.getElementById("modal-close").addEventListener("click", closeModal);
  document.getElementById("modal").addEventListener("click", (e) => {
    if (e.target.id === "modal") closeModal();
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !document.getElementById("modal").hidden) closeModal();
  });
  // Clicking a history thumb shows it large in the matching still slot.
  document.getElementById("frame-strip").addEventListener("click", (e) => {
    const img = e.target.closest("img[data-dir]");
    if (!img || !current) return;
    const slot = document.querySelector(`#modal-stills img[data-dir="${img.dataset.dir}"]`);
    if (slot) slot.src = img.src;
  });
}

function stillUrl(cam, dir, cacheBust) {
  return cam.images[dir] + (cacheBust ? "?t=" + Date.now() : "");
}

function fmtTime(d) {
  return d.toTimeString().slice(0, 8);
}

function openModal(cam) {
  closeModal(true);
  current = { cam, timer: null, frames: { N: [], E: [], S: [], W: [] } };
  document.getElementById("modal-title").textContent = cam.name;
  const stills = document.getElementById("modal-stills");
  stills.innerHTML = "";
  for (const dir of DIRS) {
    const fig = document.createElement("figure");
    const img = document.createElement("img");
    img.dataset.dir = dir;
    img.loading = "lazy";
    img.alt = `${cam.name} — ${dir}`;
    img.src = stillUrl(cam, dir, true);
    const cap = document.createElement("figcaption");
    const label = document.createElement("span");
    label.textContent = dir;
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
  const buf = current.frames[dir];
  buf.unshift(url);
  if (buf.length > 6) buf.length = 6;
}

function refreshStills() {
  if (!current) return;
  const now = fmtTime(new Date());
  for (const dir of DIRS) {
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

function renderStrip() {
  const strip = document.getElementById("frame-strip");
  strip.innerHTML = "";
  if (!current) return;
  for (const dir of DIRS) {
    for (const url of current.frames[dir]) {
      const img = document.createElement("img");
      img.loading = "lazy";
      img.dataset.dir = dir;
      img.alt = `${current.cam.name} ${dir} previous frame`;
      img.src = url;
      strip.append(img);
    }
  }
}

async function enhanceFromHistory(cam) {
  if (!historyBase || !current || current.cam.id !== cam.id) return;
  let manifest;
  try {
    const res = await fetch(historyBase + "/history/manifest.json", { cache: "no-store" });
    if (!res.ok) throw new Error("HTTP " + res.status);
    manifest = await res.json();
  } catch (err) {
    console.warn("history unavailable, using live frames:", err.message);
    return;
  }
  historyManifest = manifest;
  const entry = manifest && manifest.cams && manifest.cams[cam.id];
  if (!entry || !current || current.cam.id !== cam.id) return;
  const strip = document.getElementById("frame-strip");
  strip.innerHTML = "";
  for (const dir of DIRS) {
    for (const rel of entry[dir] || []) {
      const img = document.createElement("img");
      img.loading = "lazy";
      img.dataset.dir = dir;
      img.alt = `${cam.name} ${dir} archived frame`;
      img.src = historyBase + "/" + rel;
      strip.append(img);
    }
  }
}

function closeModal(silent) {
  if (current && current.timer) clearInterval(current.timer);
  current = null;
  if (!silent) document.getElementById("modal").hidden = true;
  else document.getElementById("modal").hidden = true;
}

if (typeof document !== "undefined" && document.getElementById("map")) {
  load().catch((err) => {
    document.getElementById("count").textContent = "failed to load cams.json";
    console.error(err);
  });
}
