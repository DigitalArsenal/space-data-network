import { mergeConfig } from 'vite';
import baseConfig from './vite.config.mts';

// The node homepage is an SDS $APP whose entry page is one self-contained HTML
// document. Keep the normal development build unchanged; this release build
// folds the application graph into one JavaScript entry chunk for the packer.
const config = mergeConfig(baseConfig, {
  define: {
    'import.meta.env.VITE_EMBEDDED_SDN_APP': 'true',
  },
  build: {
    outDir: 'dist-sdnapp',
    emptyOutDir: true,
    // The SDS $APP packer embeds the resulting entry page.  Keep binary
    // resources (including FlatSQL WASM) in that document rather than leaving
    // a relative fetch behind in the browser bundle.
    assetsInlineLimit: Number.MAX_SAFE_INTEGER,
    rollupOptions: {
      output: {
        inlineDynamicImports: true,
      },
    },
  },
  worker: {
    format: 'es',
    rollupOptions: {
      output: {
        inlineDynamicImports: true,
      },
    },
  },
});

// mergeConfig retains the normal build's manual chunk function unless it is
// explicitly removed; Rollup forbids it when inlineDynamicImports is enabled.
delete config.build?.rollupOptions?.output?.manualChunks;

export default config;
