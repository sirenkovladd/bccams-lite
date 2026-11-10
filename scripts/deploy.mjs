#!/usr/bin/env bun
// scripts/deploy.mjs — sync public/ to object storage + verify.
//
// ALL deployment targets come from the environment; nothing about where
// files land is stored in this repo:
//   R2_BUCKET            (required) bucket; keys are bare filenames at root
//   CLOUDFLARE_API_TOKEN (required) admin API token for wrangler; inherited
//                          via process env only, never printed or passed as arg
//   R2_PUBLIC_BASE_URL   (optional) public base serving the bucket; enables
//                          post-upload HTTP checks (skipped when unset)
//
// Usage (from the repo root):
//   R2_BUCKET=... CLOUDFLARE_API_TOKEN=... bun scripts/deploy.mjs
//   bun scripts/deploy.mjs --dry-run | --skip-build | --skip-verify | --help
//
// The token is only ever inherited by child processes through the
// environment. This script never logs it; errors name the variable,
// not its value.

import { $ } from "bun";
import { existsSync } from "node:fs";

const FILES = [
  ["index.html", "text/html"],
  ["app.js", "application/javascript"],
  ["style.css", "text/css"],
  ["cams.json", "application/json"],
  ["history.json", "application/json"],
];

const args = new Set(process.argv.slice(2));
if (args.has("--help") || args.has("-h")) {
  console.log(
    "usage: bun scripts/deploy.mjs [--dry-run] [--skip-build] [--skip-verify]\n" +
      "env: R2_BUCKET (required), CLOUDFLARE_API_TOKEN (required),\n" +
      "     R2_PUBLIC_BASE_URL (optional, enables HTTP checks)",
  );
  process.exit(0);
}
const dryRun = args.has("--dry-run");
const skipBuild = args.has("--skip-build") || dryRun;
const skipVerify = args.has("--skip-verify") || dryRun;

if (!existsSync("public") || !existsSync("scripts/build.mjs")) {
  console.error("deploy: run from the repo root (public/ not found)");
  process.exit(1);
}

const bucket = (process.env.R2_BUCKET || "").trim();
if (!bucket) {
  console.error("deploy: R2_BUCKET is not set (export it in your shell; never in a file)");
  process.exit(1);
}
if (!dryRun && !process.env.CLOUDFLARE_API_TOKEN) {
  console.error("deploy: CLOUDFLARE_API_TOKEN is not set (export it in your shell; never in a file)");
  process.exit(1);
}
const base = (process.env.R2_PUBLIC_BASE_URL || "").replace(/\/$/, "");

if (dryRun) {
  console.log(`deploy (dry run) -> bucket "${bucket}"; ${FILES.length} files would upload:`);
  for (const [f, ct] of FILES) console.log(`  ${f}  (${ct})`);
  console.log(
    `build: ${skipBuild ? "skipped" : "would run"}; verify: ${!skipVerify && base ? "would run" : "skipped"}`,
  );
  process.exit(0);
}

if (!skipBuild) {
  console.log("deploy: verifying cameras + regenerating data…");
  await $`bun scripts/build.mjs`;
}

console.log("deploy: uploading public/ …");
for (const [f, ct] of FILES) {
  // Token reaches wrangler through inherited process env only — never argv.
  await $`npx -y wrangler r2 object put ${`${bucket}/${f}`} --file ${`public/${f}`} --content-type ${ct} --remote`;
  console.log(`  ok ${f} (${ct})`);
}

if (!skipVerify) {
  if (!base) {
    console.log("deploy: R2_PUBLIC_BASE_URL unset — skipping HTTP checks");
  } else {
    console.log("deploy: verifying live objects…");
    let bad = 0;
    for (const [f, ct] of FILES) {
      let ok = false;
      let detail = "";
      try {
        const res = await fetch(`${base}/${f}`);
        const got = (res.headers.get("content-type") || "").split(";")[0].trim();
        ok = res.status === 200 && got === ct;
        detail = `HTTP ${res.status} content-type=${got || "(none)"}`;
      } catch (err) {
        detail = `fetch failed: ${err.message}`;
      }
      console.log(`  ${ok ? "ok" : "FAIL"} ${f}: ${detail}`);
      if (!ok) bad++;
    }
    if (bad) {
      console.error(`deploy: ${bad} object check(s) failed`);
      process.exit(1);
    }
  }
}
console.log("deploy: done");
