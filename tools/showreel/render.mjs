// Renders a reel frame by frame in Chromium and encodes it with ffmpeg.
//   node tools/showreel/render.mjs [--reel sdn] [--frames a-b] [--out dir]
//                                  [--stills t1,t2] [--media dir] [--mux-only]
//                                  [--verify]
// --reel picks tools/showreel/reels/<name>.js (default sdn). Outputs
// <media>/<name>.{mp4,webm} and <name>-poster.webp, where the reel's meta
// names the file, the default media folder and the poster frame; --media
// writes somewhere else (another stack site's repository). --mux-only re-cuts
// the soundtrack onto existing encodes without rendering frames.
// --verify is the reel's end-to-end check, with no encode: the reel's meta and
// soundtrack are sound, the first, middle, last and poster frames render (written
// to --out when given) and differ, every second of the reel renders, no competitor
// name is drawn anywhere in it, and every number the reel declares in `facts`
// names its source.
// Needs Playwright (any installed copy: set PLAYWRIGHT_MODULE to its path; else
// the one in the repository or in sdn-js is used), an ffmpeg with libx264 and
// libsvtav1, and ImageMagick (the poster).
// Options are strict: an unknown option, or one without its value, stops the run.

import { spawnSync } from "node:child_process";
import { createReadStream, existsSync, mkdirSync, mkdtempSync, rmSync, statSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, extname, join, normalize, resolve } from "node:path";
import process from "node:process";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, "..", "..");
const FPS = 30;
const TRACKS = ["orbital-insertion", "covariance", "delta-v"]; // docs/media/audio
// No competitor is named in any video (marketing law); matched case-insensitively against everything a reel draws.
const COMPETITORS = /comspoc|space force|ussf|sgp4|starlink|spacex/i;

const VALUE_FLAGS = new Set(["reel", "frames", "out", "stills", "media"]);
const BOOLEAN_FLAGS = new Set(["mux-only", "verify"]);
function parseArgs(argv) {
  const parsed = {};
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (!a.startsWith("--")) throw new Error(`unexpected argument ${a}`);
    const key = a.slice(2);
    if (BOOLEAN_FLAGS.has(key)) parsed[key] = true;
    else if (VALUE_FLAGS.has(key)) {
      const value = argv[++i];
      if (value === undefined || value.startsWith("--")) throw new Error(`--${key} needs a value`);
      parsed[key] = value;
    } else throw new Error(`unknown option --${key}`);
  }
  return parsed;
}

async function loadChromium() {
  const candidates = [];
  if (process.env.PLAYWRIGHT_MODULE) candidates.push(pathToFileURL(process.env.PLAYWRIGHT_MODULE).href);
  candidates.push("playwright");
  try {
    const found = createRequire(join(repo, "sdn-js", "package.json")).resolve("playwright");
    candidates.push(pathToFileURL(join(dirname(found), "index.mjs")).href);
  } catch {
    // no sdn-js install
  }
  for (const candidate of candidates) {
    try {
      return (await import(candidate)).chromium;
    } catch (error) {
      if (error.code !== "ERR_MODULE_NOT_FOUND") throw error;
    }
  }
  throw new Error("Playwright not found: set PLAYWRIGHT_MODULE to an installed copy's index.mjs");
}

const TYPES = { ".html": "text/html", ".js": "text/javascript", ".mjs": "text/javascript", ".webp": "image/webp", ".png": "image/png" };
// Serves the repository, and opens the reel's page in headless Chromium.
async function openReel(name) {
  const chromium = await loadChromium();
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
  await page.goto(`http://127.0.0.1:${port}/tools/showreel/showreel.html?reel=${name}`);
  await page.waitForFunction(() => window.showreelReady === true, null, { timeout: 60000 });
  const total = await page.evaluate(() => window.showreel.frames);
  const meta = await page.evaluate(() => window.showreel.meta);
  return {
    page,
    total,
    meta,
    async grab(i) {
      const data = await page.evaluate((n) => window.showreel.renderFrame(n).toDataURL("image/png"), i);
      return Buffer.from(data.split(",")[1], "base64");
    },
    async close() {
      await browser.close();
      server.close();
    },
  };
}

const ff = (argv) => {
  const r = spawnSync("ffmpeg", ["-hide_banner", "-loglevel", "error", "-y", ...argv], { stdio: "inherit" });
  if (r.status !== 0) throw new Error(`ffmpeg ${argv.join(" ")}`);
};
// The soundtrack: meta.audio = { track, start } takes the reel's length of
// docs/media/audio/<track>.mp3 from start seconds, fading in over 1 s and out
// over the last 1.5 s, and muxes it beside the untouched video stream.
function mux({ meta, total }, outDir, file, codecArgs) {
  if (!meta.audio) return;
  const duration = total / FPS;
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

async function runStills(reel, args) {
  const outDir = args.out ?? join(here, ".frames");
  mkdirSync(outDir, { recursive: true });
  for (const s of args.stills.split(",")) {
    const i = Math.round(Number(s) * FPS);
    writeFileSync(join(outDir, `still-${s}.png`), await reel.grab(i));
  }
  process.stdout.write(`stills written to ${outDir}\n`);
}

// Re-cuts the soundtrack onto the reel's existing encodes.
function runMux(reel, args) {
  const outDir = args.out ?? join(here, ".frames");
  const media = args.media ? resolve(args.media) : join(repo, reel.meta.media);
  mkdirSync(outDir, { recursive: true });
  mux(reel, outDir, join(media, `${reel.meta.name}.mp4`), AAC);
  mux(reel, outDir, join(media, `${reel.meta.name}.webm`), OPUS);
  process.stdout.write(`soundtrack muxed into ${media}\n`);
}

async function runRender(reel, args) {
  const { meta, total } = reel;
  const outDir = args.out ?? join(here, ".frames");
  const media = args.media ? resolve(args.media) : join(repo, meta.media);
  const [a, b] = (args.frames ?? `0-${total - 1}`).split("-").map(Number);
  rmSync(outDir, { recursive: true, force: true });
  mkdirSync(outDir, { recursive: true });
  const started = Date.now();
  for (let i = a; i <= b; i++) {
    writeFileSync(join(outDir, `f${String(i).padStart(4, "0")}.png`), await reel.grab(i));
    if (i % 30 === 0) process.stdout.write(`frame ${i}/${b} ${((Date.now() - started) / 1000).toFixed(0)}s\n`);
  }
  mkdirSync(media, { recursive: true });
  const input = ["-framerate", String(FPS), "-start_number", String(a), "-i", join(outDir, "f%04d.png")];
  // meta.crf = [x264, AV1]; busy reels (many fine lines changing every frame)
  // take a higher value to stay near 10-13 MB.
  const [crf264, crfAv1] = meta.crf ?? [27, 36];
  ff([...input, "-c:v", "libx264", "-preset", "slow", "-crf", String(crf264), "-profile:v", "high", "-pix_fmt", "yuv420p", "-tune", "film", "-movflags", "+faststart", join(media, `${meta.name}.mp4`)]);
  ff([...input, "-c:v", "libsvtav1", "-preset", "5", "-crf", String(crfAv1), "-pix_fmt", "yuv420p", "-svtav1-params", "tune=0", join(media, `${meta.name}.webm`)]);
  mux(reel, outDir, join(media, `${meta.name}.mp4`), AAC);
  mux(reel, outDir, join(media, `${meta.name}.webm`), OPUS);
  // Poster: the reel's chosen frame. ImageMagick, because common ffmpeg
  // builds lack a WebP encoder.
  const poster = join(outDir, `f${String(Math.min(b, meta.poster)).padStart(4, "0")}.png`);
  const pm = spawnSync("magick", [poster, "-resize", "1600x", "-quality", "82", join(media, `${meta.name}-poster.webp`)], { stdio: "inherit" });
  if (pm.status !== 0) throw new Error("poster: magick failed");
  process.stdout.write(`encoded to ${media}\n`);
}

// The reel's end-to-end check (see the header). Prints one line per check; a failed check sets the exit code.
async function runVerify(reel, args) {
  const { page, total, meta } = reel;
  const failures = [];
  const check = (ok, what) => {
    process.stdout.write(`${ok ? "ok  " : "FAIL"} ${what}\n`);
    if (!ok) failures.push(what);
  };
  const seconds = total / FPS;
  // the reel's own description and its soundtrack
  check(Boolean(meta.name && meta.media && Number.isInteger(meta.poster)), `meta names the reel (${meta.name}), its media folder (${meta.media}) and a poster frame (${meta.poster})`);
  check(meta.poster >= 0 && meta.poster < total, `poster frame ${meta.poster} is inside the ${total} frames`);
  if (meta.seconds) check(seconds >= meta.seconds[0] && seconds <= meta.seconds[1], `length ${seconds.toFixed(1)} s is inside ${meta.seconds[0]}-${meta.seconds[1]} s`);
  if (meta.audio) {
    const file = join(repo, "docs", "media", "audio", `${meta.audio.track}.mp3`);
    check(TRACKS.includes(meta.audio.track) && existsSync(file), `soundtrack ${meta.audio.track} is one of the three tracks and is on disk`);
    const probe = spawnSync("ffprobe", ["-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", file], { encoding: "utf8" });
    const trackSeconds = Number(probe.stdout);
    check(Number.isFinite(trackSeconds) && meta.audio.start >= 0 && meta.audio.start + seconds <= trackSeconds + 0.05, `soundtrack from ${meta.audio.start} s for ${seconds.toFixed(1)} s fits its ${trackSeconds.toFixed(1)} s`);
  } else check(false, "meta.audio names a soundtrack");
  // what the reel declares: provenance and every number's source
  const declared = await page.evaluate(() => ({
    provenance: window.showreel.provenance ?? null,
    facts: Object.entries(window.showreel.facts ?? {}).map(([name, f]) => ({ name, source: f.source, value: window.showreel.factValue(name) })),
  }));
  if (declared.provenance) check(/^[0-9a-f]{64}$/.test(declared.provenance.moduleSha256), `recorded module output names its module (sha256 ${String(declared.provenance.moduleSha256).slice(0, 12)})`);
  for (const f of declared.facts) {
    const finite = JSON.stringify(f.value) !== undefined && !JSON.stringify(f.value).includes("null");
    check(Boolean(f.source) && finite, `fact ${f.name} = ${JSON.stringify(f.value)} (${f.source})`);
  }
  // the first, middle, last and poster frames, then every second of the reel, with every drawn string collected
  await page.evaluate(() => {
    window.__drawn = new Set();
    const fill = CanvasRenderingContext2D.prototype.fillText;
    CanvasRenderingContext2D.prototype.fillText = function drawn(text, ...rest) {
      window.__drawn.add(String(text));
      return fill.call(this, text, ...rest);
    };
  });
  const named = [["first", 0], ["middle", Math.floor(total / 2)], ["last", total - 1], ["poster", meta.poster]];
  const keep = args.out ?? mkdtempSync(join(tmpdir(), "showreel-verify-"));
  mkdirSync(keep, { recursive: true });
  const signatures = new Map();
  for (const [label, i] of named) {
    const png = await reel.grab(i);
    writeFileSync(join(keep, `${label}-${i}.png`), png);
    const stats = await page.evaluate((n) => {
      const c = window.showreel.renderFrame(n);
      const t = document.createElement("canvas");
      t.width = 96;
      t.height = 54;
      const x = t.getContext("2d", { willReadFrequently: true });
      x.drawImage(c, 0, 0, 96, 54);
      const d = x.getImageData(0, 0, 96, 54).data;
      let sum = 0;
      let sum2 = 0;
      let sig = 0;
      for (let k = 0; k < d.length; k += 4) {
        const l = (0.2126 * d[k] + 0.7152 * d[k + 1] + 0.0722 * d[k + 2]) / 255;
        sum += l;
        sum2 += l * l;
        sig = (sig * 31 + Math.round(l * 255)) >>> 0;
      }
      const n2 = d.length / 4;
      return { mean: sum / n2, std: Math.sqrt(Math.max(0, sum2 / n2 - (sum / n2) ** 2)), sig };
    }, i);
    signatures.set(label, stats.sig);
    if (label !== "first") check(stats.mean > 0.004 && stats.std > 0.01, `${label} frame ${i} is not blank (mean ${stats.mean.toFixed(3)}, spread ${stats.std.toFixed(3)})`);
  }
  check(new Set(signatures.values()).size === named.length, "the first, middle, last and poster frames all differ");
  await page.evaluate((n) => {
    for (let i = 0; i < n; i += 30) window.showreel.renderFrame(i);
  }, total);
  const drawn = await page.evaluate(() => [...window.__drawn]);
  const named2 = drawn.filter((s) => COMPETITORS.test(s));
  check(named2.length === 0, `no competitor name among the ${drawn.length} strings the reel draws${named2.length ? `: ${named2.join(" | ")}` : ""}`);
  if (!args.out) rmSync(keep, { recursive: true, force: true });
  else process.stdout.write(`frames written to ${keep}\n`);
  if (failures.length) {
    process.stdout.write(`${failures.length} check(s) failed\n`);
    process.exitCode = 1;
  } else process.stdout.write("reel verifies\n");
}

const args = parseArgs(process.argv.slice(2));
const reel = await openReel(args.reel ?? "sdn");
try {
  if (args.verify) await runVerify(reel, args);
  else if (args.stills) await runStills(reel, args);
  else if ("mux-only" in args) runMux(reel, args);
  else await runRender(reel, args);
} finally {
  await reel.close();
}
