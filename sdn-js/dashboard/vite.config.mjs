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
 * flatsql's wasm loader imports the Node builtins `module`, `node:crypto`,
 * `fs`, `path` and `url` on its Node-only code paths. In a browser bundle
 * those specifiers are mapped to sdn-js's runtime-conditional shims (real
 * builtins in Node, inert in browsers) — the same mapping
 * scripts/build-package-entry.mjs applies to the published package entries,
 * ported to rollup's resolveId. Scoped to importers inside
 * node_modules/flatsql so no other dependency resolution changes.
 *
 * Two lanes, one mapping. The page bundle ('es') takes the shim FILES. The
 * data worker is bundled by its own rollup pass (`plugins` never reaches a
 * worker build) as an 'iife' blob: worker, and rollup refuses top-level
 * await in iife output — which is exactly how the file shims decide between
 * Node and browser. A blob: worker can never be Node, so that lane takes the
 * shims' browser branch directly: virtual modules with the same named
 * exports, all inert (`inert: true`).
 */
const FLATSQL_BUILTIN_SHIMS = new Map([
  ['module', 'flatsql-node-module'],
  ['node:crypto', 'flatsql-node-crypto'],
  ['fs', 'flatsql-node-builtins'],
  ['path', 'flatsql-node-builtins'],
  ['url', 'flatsql-node-builtins']
]);
const FLATSQL_INERT_SHIM_SOURCE = {
  'flatsql-node-module': 'export const createRequire = undefined;\nexport default null;\n',
  'flatsql-node-crypto': 'export const createHash = undefined;\nexport default null;\n',
  'flatsql-node-builtins': [
    'readFileSync', 'existsSync', 'dirname', 'join', 'fileURLToPath', 'pathToFileURL'
  ].map((name) => `export const ${name} = undefined;`).join('\n') +
    '\nexport const fs = null;\nexport const path = null;\nexport const url = null;\n'
};
function flatsqlNodeBuiltinShims({ inert = false } = {}) {
  const flatsqlDir = `node_modules${path.sep}flatsql${path.sep}`;
  const VIRTUAL = '\0sdn-dashboard:flatsql-node-shim:';
  return {
    name: `sdn-dashboard-flatsql-node-builtin-shims${inert ? '-inert' : ''}`,
    enforce: 'pre',
    resolveId(id, importer) {
      if (!importer || !importer.includes(flatsqlDir)) return null;
      const shim = FLATSQL_BUILTIN_SHIMS.get(id);
      if (!shim) return null;
      return inert ? `${VIRTUAL}${shim}` : path.resolve(__dirname, `../src/shims/${shim}.ts`);
    },
    load(id) {
      if (!id.startsWith(VIRTUAL)) return null;
      return FLATSQL_INERT_SHIM_SOURCE[id.slice(VIRTUAL.length)] ?? null;
    }
  };
}

/**
 * The worker client's DEFAULT worker — `new Worker(new URL('./local-flatsql.worker.ts',
 * import.meta.url))` in local-flatsql-worker-client.ts — is the libp2p SYNC
 * worker, the webUI's mirror lane. The dashboard never spawns it (the data
 * runtime injects the store-only worker through `createWorker`), but vite
 * bundles every `new Worker(new URL(...))` it can see, so without this the
 * embed build drags the entire libp2p sync graph through rollup for a chunk
 * no page ever loads. Vite resolves that relative URL on the filesystem (not
 * through resolveId) and then bundles the file as its own worker entry — and
 * the entry of THAT sub-build does go through resolveId, which is why this
 * plugin lives in `worker.plugins` only: it swaps the sync worker entry for a
 * stub that refuses every message, and leaves the page bundle untouched.
 */
const SYNC_WORKER_ENTRY = path.resolve(__dirname, '../src/ui/runtime/local-flatsql.worker.ts');
function stubSyncWorkerFallback() {
  const VIRTUAL = '\0sdn-dashboard:sync-worker-stub';
  return {
    name: 'sdn-dashboard-stub-sync-worker-fallback',
    enforce: 'pre',
    resolveId(id) {
      return id.split('?')[0] === SYNC_WORKER_ENTRY ? VIRTUAL : null;
    },
    load(id) {
      if (id !== VIRTUAL) return null;
      return [
        'self.onmessage = (event) => {',
        '  self.postMessage({ id: event.data?.id ?? 0, ok: false,',
        "    error: 'the libp2p sync worker is not bundled in the dashboard; the data runtime injects the store-only worker' });",
        '};',
        ''
      ].join('\n');
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
    flatsqlNodeBuiltinShims(),
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
    // Array form: a regex alias (the inlined data worker) needs it, and the
    // entries are matched in order, so the longer hd-wallet-ui key must still
    // precede the bare key.
    alias: [
      // NODE-owned transport the app imports by name (see apps/sdn-node/main.js).
      { find: 'sdn-node-status-runtime', replacement: path.resolve(__dirname, '../src/ui/runtime/status-dashboard') },
      // NODE-owned data runtime: the FlatSQL window over the node's raw
      // FlatBuffer lane (owner ruling 2026-09-03), imported by name.
      { find: 'sdn-node-data-runtime', replacement: path.resolve(__dirname, '../src/ui/runtime/dashboard-data-runtime') },
      // The store-only FlatSQL worker, imported as
      // `sdn-node-data-worker?worker&inline` so vite inlines it into the
      // single file and spawns it from a blob: URL (worker.format 'iife'
      // below). `$1` carries the ?worker&inline query through the alias.
      {
        find: /^sdn-node-data-worker(\?.*)?$/,
        replacement: path.resolve(__dirname, '../src/ui/runtime/local-flatsql-store.worker.ts') + '$1'
      },
      { find: 'hd-wallet-ui/external/style', replacement: path.join(hdWalletExternalRoot, 'wallet-ui/styles/external-panel.css') },
      { find: 'hd-wallet-ui/external', replacement: path.join(hdWalletExternalRoot, 'wallet-ui/src/external/index.js') }
    ],
    dedupe: ['svelte']
  },
  // The semantic engine and the FlatSQL data worker run in Web Workers,
  // imported with ?worker&inline so vite bakes them into the single bundle
  // and spawns them from blob: URLs — no second served file, which is what
  // keeps the single-file law and `worker-src 'self' blob:` in agreement.
  // format MUST be 'iife' (vite's default), NOT 'es': with 'es' vite spawns
  // an inline worker from a `data:text/javascript` URL, which this page's
  // `worker-src 'self' blob:` correctly refuses. 'iife' emits the blob: path
  // (atob → Blob → createObjectURL) the CSP was written for.
  // The worker bundle is its own rollup pass: the flatsql builtin shims must
  // be repeated here, and flatsql.wasm is never inlined (the worker loads it
  // from the node's /sdn-js lane with an integrity check). The data worker
  // reaches the engine through dynamic imports (`import('flatsql/wasm')`, the
  // shims' runtime-conditional builtin loads); rollup treats those as
  // code-splitting, which 'iife' forbids — inline them into the one worker
  // chunk instead.
  worker: {
    format: 'iife',
    plugins: () => [stubSyncWorkerFallback(), flatsqlNodeBuiltinShims({ inert: true })],
    rollupOptions: { output: { inlineDynamicImports: true } }
  },
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
