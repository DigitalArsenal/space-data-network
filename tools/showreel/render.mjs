// Renders a reel frame by frame in Chromium and encodes it with ffmpeg.
//   node tools/showreel/render.mjs [--reel sdn] [--frames a-b] [--out dir]
//                                  [--stills t1,t2] [--media dir] [--mux-only]
// --reel picks tools/showreel/reels/<name>.js (default sdn). Outputs
// <media>/<name>.{mp4,webm} and <name>-poster.webp, where the reel's meta
// names the file, the default media folder and the poster frame; --media
// writes somewhere else (another stack site's repository). --mux-only re-cuts
// the soundtrack onto existing encodes without rendering frames.
// Needs Playwright (any installed copy: set PLAYWRIGHT_MODULE to its path), an
// ffmpeg with libx264 and libsvtav1, and ImageMagick (the poster).

import { spawnSync } from "node:child_process";
import { createReadStream, existsSync, mkdirSync, rmSync, statSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { dirname, extname, join, normalize, resolve } from "node:path";
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
await page.goto(`http://127.0.0.1:${port}/tools/showreel/showreel.html?reel=${args.reel ?? "sdn"}`);
await page.waitForFunction(() => window.showreelReady === true, null, { timeout: 60000 });
const total = await page.evaluate(() => window.showreel.frames);
const meta = await page.evaluate(() => window.showreel.meta);

async function grab(i) {
  const data = await page.evaluate((n) => window.showreel.renderFrame(n).toDataURL("image/png"), i);
  return Buffer.from(data.split(",")[1], "base64");
}

const outDir = args.out ?? join(here, ".frames");
const media = args.media ? resolve(args.media) : join(repo, meta.media);
const ff = (argv) => {
  const r = spawnSync("ffmpeg", ["-hide_banner", "-loglevel", "error", "-y", ...argv], { stdio: "inherit" });
  if (r.status !== 0) throw new Error(`ffmpeg ${argv.join(" ")}`);
};
// The soundtrack: meta.audio = { track, start } takes the reel's length of
// docs/media/audio/<track>.mp3 from start seconds, fading in over 1 s and out
// over the last 1.5 s, and muxes it beside the untouched video stream.
function mux(file, codecArgs) {
  if (!meta.audio) return;
  const duration = total / 30;
  const silent = join(outDir, `silent${extname(file)}`);
  ff(["-i", file, "-map", "0:v", "-c", "copy", silent]);
  ff([
    "-i", silent,
    "-ss", String(meta.audio.start), "-t", String(duration), "-i", join(repo, "docs", "media", "audio", `${meta.audio.track}.mp3`),
    "-map", "0:v", "-map", "1:a", "-c:v", "copy", ...codecArgs,
    "-af", `afade=t=in:st=0:d=1,afade=t=out:st=${(duration - 1.5).toFixed(2)}:d=1.5`,
    "-shortest", ...(file.endsWith(".mp4") ? ["-movflags", "+faststart"] : []), file,
  ]);
  rmSync(silent);
}
const AAC = ["-c:a", "aac", "-b:a", "160k"];
const OPUS = ["-c:a", "libopus", "-b:a", "128k"];

if (args.stills) {
  mkdirSync(outDir, { recursive: true });
  for (const s of args.stills.split(",")) {
    const i = Math.round(Number(s) * 30);
    writeFileSync(join(outDir, `still-${s}.png`), await grab(i));
  }
  process.stdout.write(`stills written to ${outDir}\n`);
} else if ("mux-only" in args) {
  // Re-cut the soundtrack onto the reel's existing encodes.
  mkdirSync(outDir, { recursive: true });
  mux(join(media, `${meta.name}.mp4`), AAC);
  mux(join(media, `${meta.name}.webm`), OPUS);
  process.stdout.write(`soundtrack muxed into ${media}\n`);
} else {
  const [a, b] = (args.frames ?? `0-${total - 1}`).split("-").map(Number);
  rmSync(outDir, { recursive: true, force: true });
  mkdirSync(outDir, { recursive: true });
  const started = Date.now();
  for (let i = a; i <= b; i++) {
    writeFileSync(join(outDir, `f${String(i).padStart(4, "0")}.png`), await grab(i));
    if (i % 30 === 0) process.stdout.write(`frame ${i}/${b} ${((Date.now() - started) / 1000).toFixed(0)}s\n`);
  }
  mkdirSync(media, { recursive: true });
  const input = ["-framerate", "30", "-start_number", String(a), "-i", join(outDir, "f%04d.png")];
  // meta.crf = [x264, AV1]; busy reels (many fine lines changing every frame)
  // take a higher value to stay near 10-13 MB.
  const [crf264, crfAv1] = meta.crf ?? [27, 36];
  ff([...input, "-c:v", "libx264", "-preset", "slow", "-crf", String(crf264), "-profile:v", "high", "-pix_fmt", "yuv420p", "-tune", "film", "-movflags", "+faststart", join(media, `${meta.name}.mp4`)]);
  ff([...input, "-c:v", "libsvtav1", "-preset", "5", "-crf", String(crfAv1), "-pix_fmt", "yuv420p", "-svtav1-params", "tune=0", join(media, `${meta.name}.webm`)]);
  mux(join(media, `${meta.name}.mp4`), AAC);
  mux(join(media, `${meta.name}.webm`), OPUS);
  // Poster: the reel's chosen frame. ImageMagick, because common ffmpeg
  // builds lack a WebP encoder.
  const poster = join(outDir, `f${String(Math.min(b, meta.poster)).padStart(4, "0")}.png`);
  const pm = spawnSync("magick", [poster, "-resize", "1600x", "-quality", "82", join(media, `${meta.name}-poster.webp`)], { stdio: "inherit" });
  if (pm.status !== 0) throw new Error("poster: magick failed");
  process.stdout.write(`encoded to ${media}\n`);
}
await browser.close();
server.close();
