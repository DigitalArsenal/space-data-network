import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { viteSingleFile } from 'vite-plugin-singlefile';
import path from 'node:path';
import fs from 'node:fs';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

/**
 * The design repo is a GIT SUBMODULE at sdn-js/spaceaware-ui
 * (DigitalArsenal/SpaceAware-UI, branch main) — owner ruling 2026-07-30: "we
 * need DigitalArsenal/SpaceAware-UI to be the sub git repo in that folder, NOT
 * a whole separate folder that we have to keep in synch." The pin IS the
 * gitlink; there is no vendored copy and no folder-to-folder sync to drift.
 *
 * Point vite straight at the checkout so it compiles the .svelte SOURCE — ZERO
 * edits to the design tree (ZIP-SYNC LAW), import-only. The previous
 * `file:`-dependency + node_modules symlink hop resolved to the same real
 * directory; it is gone because its target repo was deleted at stack e5639ca1,
 * which left this build failing on a dangling symlink.
 *
 * Territories and the who-wins-per-class ruling: spaceaware-ui/UI_SOURCE_OF_TRUTH.md
 * (IRIS). Artifact chain: dashboard/DESIGN-SOURCE.json.
 */
/**
 * The external-wallet component home (owner 2026-08-20: hd-wallet-ui owns the
 * code, every app consumes it as a component). The dashboard face imports
 * 'hd-wallet-ui/external', which is NOT the npm pin — the npm 2.0.29 pair
 * stays the node's STAGED custody runtime (/wallet-ui/*, walletui.js), a
 * separate lane this alias never touches. Resolution goes through the design
 * repo's external-checkout registry (probe = the component barrel, so a
 * pre-move checkout is not a valid resolution; worktrees and the
 * SPACEAWARE_UI_HD_WALLET_WASM override both work), and requireExternal
 * fails the build LOUD naming every place it looked. The embed roll records
 * the resolved commit alongside the submodule pin.
 */
const { requireExternal } = await import(
  path.resolve(__dirname, '../spaceaware-ui/scripts/resolve-externals.mjs')
);
const hdWalletExternalRoot = fs.realpathSync(requireExternal('hd-wallet-wasm'));

/**
 * The sdn-js status client (createNodeStatusClient) is one module carrying both
 * the REMOTE (WebSocket /ws/status) and the local HELIA assembly paths. The
 * dashboard uses REMOTE mode only; the helia path's dynamic imports
 * (edge-discovery → hd-wallet crypto, epm-resolver → libp2p) are never reached
 * at runtime. Stub them so the single-file homepage bundle stays lean instead of
 * inlining the entire libp2p/crypto graph it never executes.
 */
function stubHeliaOnlyDeps() {
  const VIRTUAL = '\0sdn-dashboard:helia-stub';
  return {
    name: 'sdn-dashboard-stub-helia-only-deps',
    enforce: 'pre',
    resolveId(id) {
      if (/(?:^|\/)(edge-discovery|epm-resolver)(?:\.[tj]s)?$/.test(id)) return VIRTUAL;
      return null;
    },
    load(id) {
      if (id === VIRTUAL) {
        return [
          'export const DEFAULT_EDGE_RELAYS = [];',
          'export const REGIONAL_FALLBACK_RELAYS = {};',
          'export function createEPMResolver() {',
          '  return { setNode() {}, resolveByPeerID: async () => null };',
          '}',
          'export default {};'
        ].join('\n');
      }
      return null;
    }
  };
}

/**
 * Scope-hash pin + its safety net. Hashing CSS instead of the path buys
 * reproducibility but introduces a failure mode the path version could not
 * have: two DIFFERENT components whose <style> blocks are byte-identical now
 * receive the SAME scope class, and their rules stop being isolated from each
 * other. That is a style leak, and it would be invisible — the page would just
 * be subtly wrong somewhere. So record every hash we mint and fail the build if
 * one is ever claimed by two files.
 */
const cssHashOwners = new Map();
function pinnedCssHash({ css, filename, hash }) {
  const h = `svelte-${hash(css)}`;
  const owners = cssHashOwners.get(h) ?? new Set();
  owners.add(filename ?? '(unknown)');
  cssHashOwners.set(h, owners);
  return h;
}
function assertNoCssHashCollision() {
  return {
    name: 'sdn-dashboard-assert-no-css-hash-collision',
    closeBundle() {
      const collisions = [...cssHashOwners].filter(([, owners]) => owners.size > 1);
      if (collisions.length === 0) return;
      for (const [h, owners] of collisions) {
        console.error(`[dashboard] scope-hash collision ${h}:\n  ${[...owners].join('\n  ')}`);
      }
      throw new Error(
        'cssHash collision: components with byte-identical <style> share a scope class, ' +
          'so their rules are no longer isolated. Give one of them a distinguishing rule, ' +
          'or revert the cssHash pin (see the comment above).'
      );
    }
  };
}

// S5 (UI consolidation): the app source lives in the spaceaware-ui
// submodule; this file stays here because it is NODE law — the single-file
// build, the CSP composition and the import-map assertion belong to the
// artifact's owner, not the source repo (spec §1.1/§1.3).
//
// Owner 2026-08-28: the old dashboard tree is deleted; the embed builds from
// the TailAdmin tree — the same App the browser client shell mounts.
const appRoot = path.resolve(__dirname, '../spaceaware-ui/src/dashboard-tailadmin/apps/sdn-node');

// Tailwind v4 (CSS-first, @tailwindcss/vite). Resolved out of the design
// repo's own dependency tree — this build dir has no tailwind install.
const requireUi = createRequire(path.resolve(__dirname, '../spaceaware-ui/package.json'));
const tailwindcss = (await import(requireUi.resolve('@tailwindcss/vite'))).default;

export default defineConfig({
  root: appRoot,
  plugins: [
    tailwindcss(),
    stubHeliaOnlyDeps(),
    // cssHash PINNED to the component's CSS, not its path. IRIS ruling
    // 2026-07-30 (ui-design-lib-two-way-sync), option 2.
    //
    // Svelte's default is `svelte-${hash(filename ?? css)}`
    // (svelte/src/compiler/validate-options.js:77-79), and
    // @sveltejs/vite-plugin-svelte hands it the RAW ABSOLUTE id
    // (utils/compile.js:59-62 via utils/id.js:18-22,47) — the root-stripped
    // form is only used by the HMR override at compile.js:66, which never
    // fires in a production build. So by default every scoped class in this
    // page is a function of the absolute checkout path: the artifact was
    // reproducible from exactly ONE directory on ONE machine, and any agent
    // obeying the stack's build-in-your-own-worktree law produced a different
    // sha with zero content change. That is a latent reproducibility defect,
    // not a property worth keeping.
    //
    // Hashing the CSS instead makes the artifact path-independent and
    // therefore reproducible by anyone, anywhere, and makes relocations and
    // rename passes provable byte-for-byte. This deliberately diverges from
    // svelte's default; the build asserts below that it did not collide.
    svelte({ compilerOptions: { cssHash: pinnedCssHash } }),
    viteSingleFile(),
    {
      /*
       * NEVER inline .wasm into the single-file homepage. viteSingleFile's
       * recommended config raises assetsInlineLimit to ~100MB in its own
       * config hook (overriding anything set in this file's build block), so
       * the moment onnxruntime-web's ort-wasm-simd-threaded.wasm became
       * resolvable (spaceaware-ui's 2026-08-04 dep install) the 553KB embed
       * silently became 36MB of base64. The semantic worker's contract is
       * wasm-as-runtime-URL with fail-open 'unavailable' when the asset is
       * not served (semantic.worker.js:68) — enforce it AFTER the plugin.
       */
      name: 'sdn-dashboard-never-inline-wasm',
      enforce: 'post',
      config(config) {
        config.build = config.build ?? {};
        config.build.assetsInlineLimit = (filePath) => !filePath.endsWith('.wasm');
      },
    },
    assertNoCssHashCollision()
  ],
  resolve: {
    alias: {
      // NODE-owned transport the app imports by name (see apps/sdn-node/main.js).
      'sdn-node-status-runtime': path.resolve(__dirname, '../src/ui/runtime/status-dashboard'),
      // Insertion order matters: the longer style key must precede the bare key.
      'hd-wallet-ui/external/style': path.join(hdWalletExternalRoot, 'wallet-ui/styles/external-panel.css'),
      'hd-wallet-ui/external': path.join(hdWalletExternalRoot, 'wallet-ui/src/external/index.js')
    },
    dedupe: ['svelte']
  },
  // The semantic engine runs in a Web Worker (semantic.worker.js), imported
  // with ?worker&inline so vite bakes it into the single bundle and spawns it
  // from a blob: URL — no second served file, which is what keeps the
  // single-file law and `worker-src 'self' blob:` in agreement.
  // format MUST be 'iife' (vite's default), NOT 'es': with 'es' vite spawns
  // an inline worker from a `data:text/javascript` URL, which this page's
  // `worker-src 'self' blob:` correctly refuses. 'iife' emits the blob: path
  // (atob → Blob → createObjectURL) the CSP was written for.
  worker: { format: 'iife' },
  build: {
    target: 'es2022',
    /*
     * NEVER inline .wasm into the single-file homepage. viteSingleFile's
     * recommended config raises assetsInlineLimit to ~100MB, so the moment
     * onnxruntime-web's ort-wasm-simd-threaded.wasm became resolvable
     * (spaceaware-ui's 2026-08-04 dep install), the 553KB embed silently
     * became 36MB of base64. The semantic worker's contract is wasm-as-
     * runtime-URL with fail-open 'unavailable' when the asset isn't served
     * (semantic.worker.js) — keep it that way structurally.
     */

    outDir: path.resolve(__dirname, 'dist'),
    emptyOutDir: true,
    rollupOptions: {
      input: path.resolve(appRoot, 'index.html'),
      output: { entryFileNames: 'index.js' }
    }
  }
});
