import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = __dirname;
const repoRoot = path.resolve(packageRoot, "..", "..");

const orbProLocalPath =
  process.env.ORBPRO_ESM_PATH ||
  path.resolve(repoRoot, "..", "OrbPro", "Build", "OrbPro", "OrbPro.esm.js");
const orbProModuleUrl = process.env.ORBPRO_ESM_URL || "Build/OrbPro/OrbPro.esm.js";
const orbProBaseUrl = process.env.ORBPRO_BASE_URL || "Build/CesiumUnminified/";
const orbProLocalExists = fs.existsSync(orbProLocalPath);
if (!orbProLocalExists) {
  console.warn(
    `[spaceaware] OrbPro local file not found at ${orbProLocalPath}. Build continues because module loads from URL (${orbProModuleUrl}).`,
  );
}
const buildStamp = new Date().toISOString();

export default defineConfig({
  plugins: [svelte()],
  base: "./",
  define: {
    __SPACEAWARE_ORBPRO_MODULE_URL__: JSON.stringify(orbProModuleUrl),
    __SPACEAWARE_ORBPRO_BASE_URL__: JSON.stringify(orbProBaseUrl),
    __SPACEAWARE_BUILD_STAMP__: JSON.stringify(buildStamp),
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
    cssCodeSplit: false,
    assetsInlineLimit: Number.MAX_SAFE_INTEGER,
    rollupOptions: {
      output: {
        inlineDynamicImports: true,
        manualChunks: undefined,
      },
    },
  },
});
