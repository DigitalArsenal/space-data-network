// Renders the showreel frame by frame in Chromium and encodes it with ffmpeg.
//   node tools/showreel/render.mjs [--frames a-b] [--out dir] [--stills t1,t2]
// Outputs docs/media/showreel/sdn-showreel.{mp4,webm} and a poster frame.
// Needs Playwright (any installed copy: set PLAYWRIGHT_MODULE to its path) and
// an ffmpeg with libx264 and libsvtav1.

import { spawnSync } from "node:child_process";
import { createReadStream, existsSync, mkdirSync, rmSync, statSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { dirname, extname, join, normalize } from "node:path";
import process from "node:process";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, "..", "..");
const args = Object.fromEntries(
  process.argv.slice(2).reduce((acc, a, i, all) => (a.startsWith("--") ? [...acc, [a.slice(2), all[i + 1]]] : acc), []),
);
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE ? pathToFileURL(process.env.PLAYWRIGHT_MODULE).href : "playwright");

const TYPES = { ".html": "text/html", ".js": "text/javascript", ".mjs": "text/javascript", ".webp": "image/webp", ".png": "image/png" };
const server = createServer((req, res) => {
  const path = normalize(decodeURIComponent(new URL(req.url, "http://x").pathname)).replace(/^([/\\])+/, "");
  const file = join(repo, path);
  if (!file.startsWith(repo) || !existsSync(file) || statSync(file).isDirectory()) {
    res.writeHead(404).end();
    return;
  }
  res.writeHead(200, { "content-type": TYPES[extname(file)] ?? "application/octet-stream" });
  createReadStream(file).pipe(res);
});
await new Promise((r) => server.listen(0, "127.0.0.1", r));
const port = server.address().port;

const browser = await chromium.launch({ headless: true, args: ["--use-angle=metal", "--enable-gpu", "--ignore-gpu-blocklist", "--enable-unsafe-webgpu"] });
const page = await browser.newPage({ viewport: { width: 1920, height: 1080 } });
page.on("pageerror", (e) => process.stderr.write(`pageerror: ${e}\n`));
await page.goto(`http://127.0.0.1:${port}/tools/showreel/showreel.html`);
await page.waitForFunction(() => window.showreelReady === true, null, { timeout: 60000 });
const total = await page.evaluate(() => window.showreel.frames);

async function grab(i) {
  const data = await page.evaluate((n) => window.showreel.renderFrame(n).toDataURL("image/png"), i);
  return Buffer.from(data.split(",")[1], "base64");
}

const outDir = args.out ?? join(here, ".frames");
if (args.stills) {
  mkdirSync(outDir, { recursive: true });
  for (const s of args.stills.split(",")) {
    const i = Math.round(Number(s) * 30);
    writeFileSync(join(outDir, `still-${s}.png`), await grab(i));
  }
  process.stdout.write(`stills written to ${outDir}\n`);
} else {
  const [a, b] = (args.frames ?? `0-${total - 1}`).split("-").map(Number);
  rmSync(outDir, { recursive: true, force: true });
  mkdirSync(outDir, { recursive: true });
  const started = Date.now();
  for (let i = a; i <= b; i++) {
    writeFileSync(join(outDir, `f${String(i).padStart(4, "0")}.png`), await grab(i));
    if (i % 30 === 0) process.stdout.write(`frame ${i}/${b} ${((Date.now() - started) / 1000).toFixed(0)}s\n`);
  }
  const media = join(repo, "docs", "media", "showreel");
  mkdirSync(media, { recursive: true });
  const ff = (argv) => {
    const r = spawnSync("ffmpeg", ["-hide_banner", "-loglevel", "error", "-y", ...argv], { stdio: "inherit" });
    if (r.status !== 0) throw new Error(`ffmpeg ${argv.join(" ")}`);
  };
  const input = ["-framerate", "30", "-start_number", String(a), "-i", join(outDir, "f%04d.png")];
  ff([...input, "-c:v", "libx264", "-preset", "slow", "-crf", "19", "-profile:v", "high", "-pix_fmt", "yuv420p", "-tune", "film", "-movflags", "+faststart", join(media, "sdn-showreel.mp4")]);
  ff([...input, "-c:v", "libsvtav1", "-preset", "5", "-crf", "30", "-pix_fmt", "yuv420p", "-svtav1-params", "tune=0", join(media, "sdn-showreel.webm")]);
  ff(["-i", join(outDir, `f${String(Math.min(b, 432)).padStart(4, "0")}.png`), "-vf", "scale=1600:-1", "-c:v", "libwebp", "-quality", "82", join(media, "sdn-showreel-poster.webp")]);
  process.stdout.write(`encoded to ${media}\n`);
}
await browser.close();
server.close();
