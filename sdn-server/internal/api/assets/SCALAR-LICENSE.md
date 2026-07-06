# Vendored: Scalar API Reference (standalone browser bundle)

- File: `scalar.standalone.js`
- Package: `@scalar/api-reference` v1.62.4 (npm), `dist/browser/standalone.js`
- License: MIT — https://github.com/scalar/scalar
- Why vendored: the daemon's docs page (`/api/v1/docs`) must be fully
  self-contained — the SDN gateway CSP forbids any external fetch (no CDN
  scripts, styles, or fonts). The bundle is served by the daemon itself and
  initialized with `withDefaultFonts: false` so it never reaches
  `fonts.scalar.com`.
- To upgrade: `npm pack @scalar/api-reference@<version>`, copy
  `package/dist/browser/standalone.js` here, update this note, and re-verify
  in a real browser that the docs page makes zero external requests.
