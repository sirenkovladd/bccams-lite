#!/usr/bin/env bun
// Full-city build: KML -> camera pages -> verify -> data/cams.json.
// Sources (never invent URLs): the city KML, the city index (fallback only),
// camera pages, the UBC weather-cam page. Every emitted image URL is verified
// live (HEAD, GET fallback; 200 + image/*). Unverifiable cams are skipped with
// reasons in data/skipped.json; only a missing mustInclude cam fails the build.
import { writeFileSync, mkdirSync, readFileSync, existsSync } from "node:fs";
import { join } from "node:path";

const KML_URL = "https://vanmapp1.vancouver.ca/googleKml/traffic_cameras/";
const INDEX_URL = "https://trafficcams.vancouver.ca/";
const IMG_BASE = "https://trafficcams.vancouver.ca/cameraimages/";
const UBC_PAGE = "https://weather.eos.ubc.ca/wxfcst/current/cameras/index.html";
const UBC_BASE = "https://weather.eos.ubc.ca/wxfcst/current/cameras/";
const CONCURRENCY = 8;
const FETCH_TIMEOUT = 30000;

// UBC recon (spike 2026-11-10): page exposes UBC_cam{1..4}_{north,east,south,west}.jpg,
// 200 + image/jpeg verified. Coords: ESB rooftop weather station 49.2633,-123.2522.
const UBC_IMAGES = {
  N: UBC_BASE + "UBC_cam1_north.jpg",
  E: UBC_BASE + "UBC_cam2_east.jpg",
  S: UBC_BASE + "UBC_cam4_south.jpg",
  W: UBC_BASE + "UBC_cam3_west.jpg",
};

const MUST_INCLUDE_PAGES = ["burrardDavie.htm", "buteDavie.htm"];

const root = join(import.meta.dir, "..");
const REGIONS = JSON.parse(readFileSync(join(root, "data", "regions.json"), "utf8"));

function normTitle(t) {
  return t.toLowerCase().replace(/boundry/g, "boundary").replace(/\b(\d+) av\b/g, "$1st");
}

function regionFor(title) {
  const t = normTitle(title);
  for (const [region, kws] of REGIONS.rules) {
    if (kws.every((k) => t.includes(k))) return region;
  }
  return REGIONS.fallback;
}

function prettyName(h5) {
  return h5.replace(/\bAv\b/g, "Ave").replace(/\s+/g, " ").trim();
}

function slug(pageUrl) {
  const m = /\/([^/?#]+)\.htm/i.exec(pageUrl);
  return (m ? m[1] : pageUrl).toLowerCase();
}

async function get(url) {
  const res = await fetch(url, { redirect: "follow", signal: AbortSignal.timeout(FETCH_TIMEOUT) });
  if (!res.ok) throw new Error(`${url} -> HTTP ${res.status}`);
  return await res.text();
}

async function verify(url) {
  let res;
  try {
    res = await fetch(url, {
      method: "HEAD",
      redirect: "follow",
      signal: AbortSignal.timeout(FETCH_TIMEOUT),
    });
    if (res.status === 405) {
      res = await fetch(url, { redirect: "follow", signal: AbortSignal.timeout(FETCH_TIMEOUT) });
    }
  } catch (err) {
    throw new Error(`${url} -> fetch failed: ${err.message}`);
  }
  if (res.body) {
    try {
      await res.arrayBuffer();
    } catch {
      /* ignore */
    }
  }
  const ct = res.headers.get("content-type") || "";
  if (res.status !== 200 || !ct.startsWith("image/")) {
    throw new Error(`${url} -> HTTP ${res.status} content-type=${ct || "(none)"}`);
  }
  return { lastModified: res.headers.get("last-modified") || "", etag: res.headers.get("etag") || "" };
}

function parseKml(xml) {
  const out = [];
  // Descriptions are HTML-escaped; unescape the entities we need for parsing.
  const clean = xml.replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");
  for (const m of clean.matchAll(/<Placemark id="([^"]+)">(.*?)<\/Placemark>/gs)) {
    const [_, id, body] = m;
    const title = /<h5>(.*?)<\/h5>/s.exec(body)?.[1]?.trim() ?? id;
    const page = /href='(https:\/\/trafficcams\.vancouver\.ca\/[^']+)'/.exec(body)?.[1]?.trim() ?? null;
    const coords = /<coordinates>([^<]+)<\/coordinates>/.exec(body)?.[1]?.trim().split(",") ?? [];
    out.push({ kmlId: id, title, page, lng: parseFloat(coords[0]), lat: parseFloat(coords[1]) });
  }
  return out;
}

function indexHrefs(indexHtml) {
  const out = [];
  for (const m of indexHtml.matchAll(/<a\s[^>]*href="([^"]*\.htm[^"]*)"[^>]*>(.*?)<\/a>/gis)) {
    const text = m[2].replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();
    out.push({ href: m[1].trim(), text });
  }
  return out;
}

function wordSet(s) {
  return new Set(
    normTitle(s)
      .replace(/[^a-z0-9 ]/g, " ")
      .split(/\s+/)
      .filter(Boolean)
      .map((w) => (w === "av" || w === "ave" ? "ave" : w)),
  );
}

function sameWords(a, b) {
  const A = wordSet(a),
    B = wordSet(b);
  if (A.size !== B.size) return false;
  for (const w of A) if (!B.has(w)) return false;
  return true;
}

// Direction rule, first hit wins per image:
// (1) trailing "- North|East|South|West" of the alt text (anchored to the end of
//     the alt attribute, so "... - East intersection - North" votes North, not East); else
// (2) nearest preceding <strong>North|East|South|West</strong> (within 600 chars); else
// (3) /(north|east|south|west)/i in the filename.
function extractImages(pageHtml) {
  const found = {};
  const re = /<img\b[^>]*src="(cameraimages\/[^"]+)"[^>]*>/gi;
  let m;
  while ((m = re.exec(pageHtml)) !== null) {
    const [tag, rel] = m;
    const url = IMG_BASE + rel.split("/").pop();
    const start = Math.max(0, m.index - 600);
    const before = pageHtml.slice(start, m.index);
    const alt = /alt="([^"]*)"/i.exec(tag)?.[1] ?? "";
    let dir =
      /-\s*(North|East|South|West)\s*$/i.exec(alt)?.[1]?.[0]?.toUpperCase() ??
      [...before.matchAll(/<strong>(North|East|South|West)<\/strong>/gi)].pop()?.[1]?.[0]?.toUpperCase() ??
      /(north|east|south|west)/i.exec(rel)?.[1]?.[0]?.toUpperCase() ??
      null;
    if (dir && !found[dir]) found[dir] = url;
  }
  return found;
}

async function pool(items, n, fn) {
  const results = new Array(items.length);
  let i = 0;
  await Promise.all(
    Array.from({ length: Math.min(n, items.length) }, async () => {
      while (i < items.length) {
        const idx = i++;
        try {
          results[idx] = { ok: true, value: await fn(items[idx]) };
        } catch (err) {
          results[idx] = { ok: false, error: err };
        }
      }
    }),
  );
  return results;
}

const KML_CACHE = join(root, "data", "traffic_cameras.kml");
const kmlMode = process.env.KML_SOURCE === "cache" ? "cache" : "live";
let kmlSource = "CACHE";
let kmlXml = "";
if (kmlMode === "live") {
  try {
    kmlXml = await get(KML_URL);
    mkdirSync(join(root, "data"), { recursive: true });
    writeFileSync(KML_CACHE, kmlXml);
    kmlSource = "LIVE";
  } catch (err) {
    if (!existsSync(KML_CACHE)) throw err;
    console.warn(`KML live fetch failed (${err.message}); using committed cache`);
    kmlXml = readFileSync(KML_CACHE, "utf8");
  }
} else {
  kmlXml = readFileSync(KML_CACHE, "utf8");
}
const placemarks = parseKml(kmlXml);
console.log(`KML [${kmlSource}]: ${placemarks.length} placemarks`);

// Index hrefs: fallback page lookup for placemarks without page links (e.g. TCM106).
let indexLinks = [];
try {
  indexLinks = indexHrefs(await get(INDEX_URL));
} catch (err) {
  console.warn(`index fetch failed (${err.message}); pageless placemarks will be skipped`);
}

const skipped = [];
const cams = [];

for (const p of placemarks) {
  if (!p.page) {
    const hit = indexLinks.find((l) => sameWords(l.text, p.title));
    if (hit) {
      p.page = "https://trafficcams.vancouver.ca/" + hit.href.replace(/^\//, "");
      console.log(`fallback: ${p.kmlId} "${p.title}" -> ${p.page}`);
    } else {
      skipped.push({ id: p.kmlId, name: p.title, reason: "KML placemark has no camera page link" });
      continue;
    }
  }
}

const withPages = placemarks.filter((p) => p.page);
const pageResults = await pool(withPages, CONCURRENCY, async (p) => {
  const html = await get(p.page);
  const images = extractImages(html);
  const dirs = Object.keys(images);
  if (dirs.length === 0) throw new Error(`${p.page}: no camera images found`);
  return { images, dirs };
});

let verifiedCount = 0;
const verifyJobs = [];
withPages.forEach((p, i) => {
  const r = pageResults[i];
  if (!r.ok) {
    skipped.push({ id: p.kmlId, name: p.title, reason: `page fetch/parse failed: ${r.error.message}` });
    return;
  }
  for (const [dir, url] of Object.entries(r.value.images)) {
    verifyJobs.push({ p, dir, url });
  }
});

const verifyResults = await pool(verifyJobs, CONCURRENCY * 2, async (j) => verify(j.url));
const goodImages = new Map(); // page -> {dir: url}
verifyResults.forEach((r, i) => {
  const j = verifyJobs[i];
  if (!r.ok) {
    console.warn(`unverified: ${j.url} (${r.error.message})`);
    return;
  }
  verifiedCount++;
  if (!goodImages.has(j.p.page)) goodImages.set(j.p.page, {});
  goodImages.get(j.p.page)[j.dir] = j.url;
});

for (const p of withPages) {
  const images = goodImages.get(p.page);
  if (!images || Object.keys(images).length === 0) {
    skipped.push({ id: p.kmlId, name: p.title, reason: "no verified images" });
    continue;
  }
  cams.push({
    id: slug(p.page),
    name: prettyName(p.title),
    neighbourhood: regionFor(p.title),
    page: p.page,
    lat: p.lat,
    lng: p.lng,
    images,
  });
}

// UBC weather cam (same shape; recon 2026-11-10 in task brief).
for (const [dir, url] of Object.entries(UBC_IMAGES)) {
  try {
    await verify(url);
    verifiedCount++;
  } catch (err) {
    skipped.push({ id: "ubc-weather", name: "UBC Weather Camera", reason: `UBC image failed: ${err.message}` });
  }
}
if (!skipped.some((s) => s.id === "ubc-weather")) {
  cams.push({
    id: "ubc-weather",
    name: "UBC Weather Camera",
    neighbourhood: "UBC",
    page: UBC_PAGE,
    lat: 49.2633,
    lng: -123.2522,
    images: { ...UBC_IMAGES },
  });
}

// Must-include gate (bccams.ca regression): Burrard & Davie, Bute & Davie.
const byPage = new Map(cams.map((c) => [c.page.split("/").pop(), c]));
const failures = [];
for (const must of MUST_INCLUDE_PAGES) {
  const c = byPage.get(must);
  if (!c || !["N", "E", "S", "W"].every((d) => c.images[d])) {
    failures.push(must);
  }
}
if (failures.length > 0) {
  console.error(`MUST-INCLUDE MISSING: ${failures.join(", ")}`);
  process.exit(1);
}

// Order: region order, then name.
const order = new Map(REGIONS.order.map((r, i) => [r, i]));
cams.sort(
  (a, b) =>
    (order.get(a.neighbourhood) ?? 999) - (order.get(b.neighbourhood) ?? 999) ||
    a.name.localeCompare(b.name),
);

const payload = JSON.stringify(cams, null, 2) + "\n";
for (const rel of ["data/cams.json", "public/cams.json"]) {
  const out = join(root, rel);
  mkdirSync(join(root, "data"), { recursive: true });
  writeFileSync(join(root, rel), payload);
}
writeFileSync(join(root, "data", "skipped.json"), JSON.stringify(skipped, null, 2) + "\n");
console.log(
  `wrote data/cams.json (${cams.length} cams, ${verifiedCount} images verified, ${skipped.length} skipped) [${kmlSource}]`,
);
