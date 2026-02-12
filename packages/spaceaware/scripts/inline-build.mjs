import path from "node:path";
import { fileURLToPath } from "node:url";
import { access, readFile, rm, writeFile } from "node:fs/promises";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, "..");
const distDir = path.join(packageRoot, "dist");
const indexPath = path.join(distDir, "index.html");
const singleFilePath = path.join(distDir, "spaceaware.single.html");
const assetsDir = path.join(distDir, "assets");

function toAssetPath(href) {
  const clean = href.replace(/^\.\//, "").replace(/^\//, "");
  return path.join(distDir, clean);
}

async function inlineStyles(html) {
  const styleLinks = [...html.matchAll(/<link[^>]+rel="stylesheet"[^>]+href="([^"]+)"[^>]*>/g)];
  let out = html;

  for (const match of styleLinks) {
    const fullTag = match[0];
    const href = match[1];
    const cssPath = toAssetPath(href);
    const css = await readFile(cssPath, "utf8");
    out = out.replace(fullTag, () => `<style>\n${css}\n</style>`);
  }

  return out;
}

async function inlineModuleScript(html) {
  const scriptMatch = html.match(
    /<script[^>]+type="module"[^>]+src="([^"]+)"[^>]*><\/script>/,
  );
  if (!scriptMatch) {
    return html;
  }

  const fullTag = scriptMatch[0];
  const src = scriptMatch[1];
  const jsPath = toAssetPath(src);
  const js = await readFile(jsPath, "utf8");

  return html.replace(fullTag, () => `<script type="module">\n${js}\n</script>`);
}

async function buildSingleFile() {
  let html = await readFile(indexPath, "utf8");
  html = await inlineStyles(html);
  html = await inlineModuleScript(html);

  await writeFile(indexPath, html, "utf8");
  await writeFile(singleFilePath, html, "utf8");

  try {
    await access(assetsDir);
    await rm(assetsDir, { recursive: true, force: true });
  } catch {
    // No assets directory to clean.
  }

  console.log("SpaceAware single-file output ready:");
  console.log(`- ${indexPath}`);
  console.log(`- ${singleFilePath}`);
}

buildSingleFile().catch((error) => {
  console.error("[spaceaware] Failed to inline build output:");
  console.error(error);
  process.exit(1);
});
