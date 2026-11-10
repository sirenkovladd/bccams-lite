# bccams-lite — agent notes

- Read `README.md` for why, what to build, and acceptance.
- Camera source is the city KML (`data/traffic_cameras.kml`). Never invent image URLs. Verify every URL with a HEAD/GET in `scripts/build.mjs`; fail the build on unverified.
- Layout to create: `public/` (`index.html`, `app.js`, `style.css`), `scripts/build.mjs` (bun), `data/cams.json` (generated, committed).
- Constraints: static only, no backend, no proxy, no keys, no secrets in output.
- Temp captures and probe scripts go in `$TMPDIR`, never the repo.
- Deploy: `bun scripts/deploy.mjs` from the repo root (build → upload `public/` → HTTP verify).
- Deploy targets live ONLY in the environment, never in repo files: `R2_BUCKET` (required), `CLOUDFLARE_API_TOKEN` (required admin token for wrangler; inherited via process env, never logged), `R2_PUBLIC_BASE_URL` (optional; enables post-upload checks, skipped when unset).
- Flags: `--dry-run` (plan only; needs just `R2_BUCKET`), `--skip-build`, `--skip-verify`, `--help`.
- Operator exports the vars in their own shell from their own secret store before running. Never echo tokens, never commit `.env`.
- Checks: `bun scripts/build.mjs`, then serve `public/` and open it. Gate is the acceptance list in `README.md`.
