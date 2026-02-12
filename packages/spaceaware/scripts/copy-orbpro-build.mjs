import path from "node:path";
import { fileURLToPath } from "node:url";
import { cp, mkdir, rm, stat } from "node:fs/promises";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, "..");
const repoRoot = path.resolve(packageRoot, "..", "..");
const distRoot = path.join(packageRoot, "dist");
const distBuildRoot = path.join(distRoot, "Build");

const orbProBuildRoot = process.env.ORBPRO_BUILD_ROOT || path.resolve(repoRoot, "..", "OrbPro", "Build");
const orbProModuleDir = process.env.ORBPRO_MODULE_DIR || path.join(orbProBuildRoot, "OrbPro");
const orbProBaseDir =
  process.env.ORBPRO_BASE_DIR || path.join(orbProBuildRoot, "CesiumUnminified");

function log(message) {
  console.log(`[spaceaware:ipfs] ${message}`);
}

async function assertPathExists(targetPath, kind) {
  let info;
  try {
    info = await stat(targetPath);
  } catch {
    throw new Error(`Missing ${kind}: ${targetPath}`);
  }
  if (kind === "directory" && !info.isDirectory()) {
    throw new Error(`Expected directory at ${targetPath}`);
  }
  if (kind === "file" && !info.isFile()) {
    throw new Error(`Expected file at ${targetPath}`);
  }
}

async function copyBuildAssets() {
  await assertPathExists(path.join(orbProModuleDir, "OrbPro.esm.js"), "file");
  await assertPathExists(path.join(orbProBaseDir, "Workers"), "directory");
  await assertPathExists(distRoot, "directory");

  const distModuleDir = path.join(distBuildRoot, "OrbPro");
  const distBaseDir = path.join(distBuildRoot, "CesiumUnminified");

  await mkdir(distBuildRoot, { recursive: true });
  await rm(distModuleDir, { recursive: true, force: true });
  await rm(distBaseDir, { recursive: true, force: true });

  log(`Copying OrbPro module from ${orbProModuleDir}`);
  await cp(orbProModuleDir, distModuleDir, { recursive: true });

  log(`Copying Cesium runtime assets from ${orbProBaseDir}`);
  await cp(orbProBaseDir, distBaseDir, { recursive: true });

  log(`IPFS bundle assets ready at ${distBuildRoot}`);
}

copyBuildAssets().catch((error) => {
  console.error("[spaceaware:ipfs] Failed to prepare IPFS bundle assets");
  console.error(error);
  process.exit(1);
});
