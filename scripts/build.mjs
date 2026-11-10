#!/usr/bin/env bun
// Build: verify every camera image URL live, write data/cams.json.
// Fails (nonzero exit) on any unverified URL. Never invent URLs —
// this table is the verified set (seed + scraped West End pages).
import { writeFileSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";

const IMG = "https://trafficcams.vancouver.ca/cameraimages/";

const CAMS = [
  {
    id: "burrard-davie",
    name: "Burrard St & Davie St",
    neighbourhood: "West End",
    page: "https://trafficcams.vancouver.ca/burrardDavie.htm",
    lat: 49.2791,
    lng: -123.1292,
    images: {
      N: IMG + "BurrardDavieNorth.jpg",
      E: IMG + "BurrardDavieEast.jpg",
      S: IMG + "BurrardDavieSouth.jpg",
      W: IMG + "BurrardDavieWest.jpg",
    },
  },
  {
    id: "bute-davie",
    name: "Bute St & Davie St",
    neighbourhood: "West End",
    page: "https://trafficcams.vancouver.ca/buteDavie.htm",
    lat: 49.2818,
    lng: -123.1334,
    images: {
      N: IMG + "ButeNorthDavie.jpg",
      E: IMG + "DavieEastBute.jpg",
      S: IMG + "ButeSouthDavie.jpg",
      W: IMG + "DavieWestBute.jpg",
    },
  },
  {
    id: "burrard-drake",
    name: "Burrard St & Drake St",
    neighbourhood: "West End",
    page: "https://trafficcams.vancouver.ca/burrardDrake.htm",
    lat: 49.2809,
    lng: -123.1289,
    images: {
      N: IMG + "BurrardDrakeNorth.jpg",
      E: IMG + "BurrardDrakeEast.jpg",
      S: IMG + "BurrardDrakeSouth.jpg",
      W: IMG + "BurrardDrakeWest.jpg",
    },
  },
  {
    id: "burrard-pacific",
    name: "Burrard St & Pacific Ave",
    neighbourhood: "West End",
    page: "https://trafficcams.vancouver.ca/BurrardPacific4.htm",
    lat: 49.2775,
    lng: -123.1285,
    images: {
      N: IMG + "BurrardPacificNorth.jpg",
      E: IMG + "BurrardPacificEast.jpg",
      S: IMG + "BurrardBridgeSouth.jpg",
      W: IMG + "BurrardPacificWest.jpg",
    },
  },
];

async function verify(url) {
  let res;
  try {
    res = await fetch(url, { method: "HEAD", redirect: "follow" });
    if (res.status === 405) res = await fetch(url, { redirect: "follow" });
  } catch (err) {
    throw new Error(`${url} -> fetch failed: ${err.message}`);
  }
  // Drain GET bodies so connections close cleanly.
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
}

const failures = [];
for (const cam of CAMS) {
  for (const [dir, url] of Object.entries(cam.images)) {
    try {
      await verify(url);
      console.log(`ok ${cam.id}/${dir}`);
    } catch (err) {
      console.error(`FAIL ${err.message}`);
      failures.push(url);
    }
  }
}

if (failures.length > 0) {
  console.error(`${failures.length} URL(s) failed verification`);
  process.exit(1);
}

const payload = JSON.stringify(CAMS, null, 2) + "\n";
// data/cams.json = committed source of truth; public/cams.json = runtime copy
// served alongside app.js (static hosts serve public/ only).
for (const rel of ["data/cams.json", "public/cams.json"]) {
  const out = join(import.meta.dir, "..", rel);
  mkdirSync(dirname(out), { recursive: true });
  writeFileSync(out, payload);
}
console.log(`wrote data/cams.json + public/cams.json (${CAMS.length} cams, 16 images verified)`);
