import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = __dirname;
const repoRoot = path.resolve(packageRoot, "..", "..");

const orbProPath =
  process.env.ORBPRO_ESM_PATH ||
  path.resolve(repoRoot, "..", "OrbPro", "Build", "OrbPro", "OrbPro.esm.js");

function loadOrbProSource() {
  try {
    return fs.readFileSync(orbProPath, "utf8");
  } catch (error) {
    console.warn(
      `[spaceaware] Failed to read OrbPro bundle at ${orbProPath}. Build will keep a placeholder.`,
    );
    return "__ORBPRO_ESM_SOURCE__";
  }
}

const orbProSource = loadOrbProSource();
const buildStamp = new Date().toISOString();

export default defineConfig({
  plugins: [svelte()],
  base: "./",
  define: {
    __ORBPRO_ESM_SOURCE__: JSON.stringify(orbProSource),
    __SPACEAWARE_BUILD_STAMP__: JSON.stringify(buildStamp),
    __SPACEAWARE_ORBPRO_PATH__: JSON.stringify(orbProPath),
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
