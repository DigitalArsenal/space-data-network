// Renders the whitepaper intro reels with the showreel renderer and names the
// outputs docs/media/papers/<paper>/{reel.webm, reel.mp4, poster.webp}.
//   PLAYWRIGHT_MODULE=<dir>/node_modules/playwright/index.mjs \
//     node tools/paper-reels/render.mjs [paper ...] [--out <frames dir>] [--stills t1,t2]
// Papers default to all three; each is the whitepaper's markdown basename.
// --stills writes PNG stills to <frames dir>/<paper>/ instead of encoding.

import { spawnSync } from "node:child_process";
import { existsSync, renameSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const PAPERS = ["evidence-supported-aso-catalog", "fast-conjunction-assessment", "adversarial-security"];
const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, "..", "..");
const argv = process.argv.slice(2);
const opt = (name) => {
  const i = argv.indexOf(`--${name}`);
  return i < 0 ? null : argv.splice(i, 2)[1];
};
const out = opt("out") ?? join(tmpdir(), "paper-reels");
const stills = opt("stills");
const papers = argv.length ? argv : PAPERS;

for (const paper of papers) {
  if (!PAPERS.includes(paper)) throw new Error(`unknown paper ${paper}`);
  const args = [join(repo, "tools", "showreel", "render.mjs"), "--reel", `../../paper-reels/${paper}`, "--out", join(out, paper)];
  if (stills) args.push("--stills", stills);
  const r = spawnSync(process.execPath, args, { stdio: "inherit" });
  if (r.status !== 0) throw new Error(`render ${paper} failed`);
  if (stills) continue;
  const media = join(repo, "docs", "media", "papers", paper);
  if (existsSync(join(media, "reel-poster.webp"))) renameSync(join(media, "reel-poster.webp"), join(media, "poster.webp"));
}
