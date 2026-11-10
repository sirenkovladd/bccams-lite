# bccams-lite — agent notes

- Read `README.md` for why, then `PROMPT.md` for what to build and acceptance.
- Seed (verified URLs only): `data/seed.json`. Expand by scraping the source index pages listed there.
- Never invent image URLs. Verify every URL with a HEAD/GET in `scripts/build.mjs`; fail the build on unverified.
- Layout to create: `public/` (`index.html`, `app.js`, `style.css`), `scripts/build.mjs` (bun), `data/cams.json` (generated, committed).
- Constraints: static only, no backend, no proxy, no keys, no secrets in output.
- Temp captures and probe scripts go in `$TMPDIR`, never the repo.
- Checks: `bun scripts/build.mjs`, then serve `public/` and open it. Gate is the acceptance list in `PROMPT.md`.
