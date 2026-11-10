// Regression pins for the full-city build (plan Review Focus 1-2 + emit invariants).
// Hermetic: fixtures + committed artifacts only, no network.
import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import {
  extractImages,
  indexHrefs,
  parseKml,
  prettyName,
  regionFor,
  sameWords,
  slug,
  stampAssets,
} from "./build.mjs";

const root = join(import.meta.dir, "..");

// Pages resolved via the city index fallback (no KML page link), not the KML.
const INDEX_FALLBACK_IDS = new Set(["ruperte22"]);

describe("parseKml", () => {
  test("placemark with page link vs pageless placemark", () => {
    const xml = `<kml><Document>
<Placemark id="TCM001"><name>x</name><description>&lt;h5&gt;Burrard St and Davie St&lt;/h5&gt;&lt;a href='https://trafficcams.vancouver.ca/BurrardDavie.htm'&gt;link&lt;/a&gt;</description><Point><coordinates>-123.1,49.2,0</coordinates></Point></Placemark>
<Placemark id="TCM106"><name>y</name><description>&lt;h5&gt;Rupert St and E 22nd Av&lt;/h5&gt;</description><Point><coordinates>-123.0,49.25,0</coordinates></Point></Placemark>
</Document></kml>`;
    expect(parseKml(xml)).toEqual([
      {
        kmlId: "TCM001",
        title: "Burrard St and Davie St",
        page: "https://trafficcams.vancouver.ca/BurrardDavie.htm",
        lng: -123.1,
        lat: 49.2,
      },
      {
        kmlId: "TCM106",
        title: "Rupert St and E 22nd Av",
        page: null,
        lng: -123.0,
        lat: 49.25,
      },
    ]);
  });

  test("committed cache parses to 220 placemarks", () => {
    const marks = parseKml(readFileSync(join(root, "data", "traffic_cameras.kml"), "utf8"));
    expect(marks.length).toBe(220);
  });

  test("page-derived slugs are unique across the cache (no id collisions)", () => {
    const marks = parseKml(readFileSync(join(root, "data", "traffic_cameras.kml"), "utf8"));
    const slugs = marks.filter((p) => p.page).map((p) => slug(p.page));
    expect(slugs.length).toBeGreaterThan(200);
    expect(new Set(slugs).size).toBe(slugs.length);
  });
});

describe("emit invariants on committed cams.json", () => {
  const cams = JSON.parse(readFileSync(join(root, "data/cams.json"), "utf8"));

  test("id === slug(page) and ids unique (UBC is the hardcoded exception)", () => {
    for (const c of cams) {
      if (c.id === "ubc-weather") continue;
      expect(c.id).toBe(slug(c.page));
    }
    expect(new Set(cams.map((c) => c.id)).size).toBe(cams.length);
  });

  test("UBC special case: fixed id, page, coords, four dirs", () => {
    const u = cams.find((c) => c.id === "ubc-weather");
    expect(u.page).toBe("https://weather.eos.ubc.ca/wxfcst/current/cameras/index.html");
    expect([u.lat, u.lng]).toEqual([49.2633, -123.2522]);
    expect(Object.keys(u.images).sort()).toEqual(["E", "N", "S", "W"]);
  });

  test("every city id resolves to a KML page or a known index fallback", () => {
    const marks = parseKml(readFileSync(join(root, "data", "traffic_cameras.kml"), "utf8"));
    const cacheSlugs = new Set(marks.filter((p) => p.page).map((p) => slug(p.page)));
    for (const c of cams) {
      if (c.id === "ubc-weather") continue;
      expect(cacheSlugs.has(c.id) || INDEX_FALLBACK_IDS.has(c.id)).toBe(true);
    }
  });

  // Mapping runs on KML titles BEFORE prettyName: Av->Ave would break the
  // 41st keyword ("41 Av"->"41st" rule), and the Boundry typo rule expects
  // the raw KML spelling. Spots below use KML-title form, verified live.
  test("regionFor maps KML-title form (incl. digit and typo rules)", () => {
    expect(regionFor("Ross St and E 41 Av")).toBe("Killarney");
    expect(regionFor("Burrard St and Davie St")).toBe("West End");
    expect(regionFor("Boundry Road and E 49th Av")).toBe("Killarney");
    expect(regionFor("Kinross St and Marine Way")).toBe("Killarney");
    expect(regionFor("Rupert St and E 22nd Av")).toBe("Renfrew-Collingwood");
  });

  test("regression intersections ship full N/E/S/W", () => {
    for (const id of ["burrarddavie", "butedavie"]) {
      const c = cams.find((x) => x.id === id);
      expect(Object.keys(c.images).sort()).toEqual(["E", "N", "S", "W"]);
    }
  });
});

describe("extractImages direction rules", () => {
  test("alt-suffix wins; preceding <strong> next; first hit wins per dir", () => {
    const html = `
      <strong>North</strong>
      <img src="cameraimages/Cambie2East.jpg" alt="Cambie St and 2nd Av - East intersection - North">
      <strong>South</strong>
      <img src="cameraimages/Cambie2West.jpg" alt="Cambie St and 2nd Av">
      <img src="cameraimages/Cambie2North.jpg" alt="Cambie St and 9th Av - North">
    `;
    expect(extractImages(html)).toEqual({
      N: "https://trafficcams.vancouver.ca/cameraimages/Cambie2East.jpg",
      S: "https://trafficcams.vancouver.ca/cameraimages/Cambie2West.jpg",
    });
  });

  test("two-dir bridge record keeps exactly its verified dirs", () => {
    const html = `
      <img src="cameraimages/DenmanBeachWest.jpg" alt="Denman - West">
      <img src="cameraimages/DenmanBeachEast.jpg" alt="Denman - East">
    `;
    expect(Object.keys(extractImages(html)).sort()).toEqual(["E", "W"]);
  });
});

describe("matching helpers", () => {
  test("sameWords ignores Av/Ave spelling and punctuation", () => {
    expect(sameWords("Rupert St and E 22nd Av", "Rupert St and E 22nd Ave")).toBe(true);
    expect(sameWords("Burrard St and Davie St", "Bute St and Davie St")).toBe(false);
  });

  test("indexHrefs extracts href + cleaned text", () => {
    const links = indexHrefs('<a href="/RupertE22.htm"><b>Rupert St</b> and E 22nd Av</a>');
    expect(links).toEqual([{ href: "/RupertE22.htm", text: "Rupert St and E 22nd Av" }]);
  });

  test("slug lowercases the page filename", () => {
    expect(slug("https://trafficcams.vancouver.ca/BurrardDavie.htm")).toBe("burrarddavie");
  });

  test("prettyName normalizes Av to Ave", () => {
    expect(prettyName("Rupert St and E 22 Av")).toBe("Rupert St and E 22 Ave");
  });

  test("regionFor falls back for unknown titles", () => {
    const fallback = JSON.parse(readFileSync(join(root, "data", "regions.json"), "utf8")).fallback;
    expect(regionFor("Somewhere With No Keywords 12345")).toBe(fallback);
  });
});


test("stampAssets pins hashed asset URLs and is idempotent", () => {
  const first = stampAssets();
  for (const f of ["app.js", "style.css"]) {
    expect(first[f]).toMatch(/^[0-9a-f]{8}$/);
  }
  const html = readFileSync(join(import.meta.dir, "..", "public", "index.html"), "utf8");
  expect(html).toContain(`app.js?h=${first["app.js"]}`);
  expect(html).toContain(`style.css?h=${first["style.css"]}`);
  const second = stampAssets();
  expect(second).toEqual(first);
});
