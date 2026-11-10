# bccams-lite

Static, no-backend alternative to [bccams.ca](https://www.bccams.ca) — Vancouver traffic/weather cameras as plain HTML+JS. No PHP, no server RAM needed.

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

Scaffold. An agent implements v1 per `PROMPT.md`. Seed data (verified URLs only) in `data/seed.json`.

## Deploy (target)

```sh
bun scripts/build.mjs
npx wrangler pages deploy public --project-name bccams-lite
# or on a 512MB box:
caddy file-server --root public --listen :80
```
