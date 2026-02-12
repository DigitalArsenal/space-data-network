# `@spacedatanetwork/spaceaware`

Svelte 5 single-file SpaceAware beta homepage for `spaceaware.io`.

## Architecture

- Framework: Svelte 5 + runes (`<svelte:options runes={true} />`)
- State model: modular stores under `src/lib/stores/*`
- Feature modules:
  - `src/lib/api/dataApi.js`
  - `src/lib/share/shareLinks.js`
  - `src/lib/orbpro/orbProLoader.js`
- Component modules:
  - `src/lib/components/Hero.svelte`
  - `src/lib/components/FreeQueryConsole.svelte`
  - `src/lib/components/OrbProPanel.svelte`
  - `src/lib/components/FreeFeatures.svelte`
  - `src/lib/components/AppFooter.svelte`

## Free-tier features

- API console for:
  - `GET /api/v1/data/health`
  - `GET /api/v1/data/omm`
  - `GET /api/v1/data/mpe` (OMM-backed emission)
  - `GET /api/v1/data/cat`
- FlatBuffers-first querying (`format=flatbuffers` default)
- JSON debugging mode (`format=json`)
- Beta link sharing (deep-link query state)
- OrbPro globe loaded from URL (`Build/OrbPro/OrbPro.esm.js`)

## Scripts

- `npm run dev`: local dev server
- `npm run build`: Vite build + inline to single-file artifact
- `npm run build:ipfs`: build + copy OrbPro runtime assets into `dist/Build`
- `npm run preview`: preview build output
- `npm run serve`: static serve `dist/`

## Build

From repo root:

```bash
npm --prefix packages/spaceaware install
npm --prefix packages/spaceaware run build
```

Outputs:

- `packages/spaceaware/dist/index.html`
- `packages/spaceaware/dist/spaceaware.single.html`

Both are single-file HTML artifacts that lazily import OrbPro from `Build/*`.

### OrbPro runtime URLs

Defaults:

- Module URL: `Build/OrbPro/OrbPro.esm.js`
- Base URL (workers/assets): `Build/CesiumUnminified/`

Override at build time:

```bash
ORBPRO_ESM_URL=Build/OrbPro/OrbPro.esm.js \
ORBPRO_BASE_URL=Build/CesiumUnminified/ \
npm --prefix packages/spaceaware run build
```

### OrbPro local validation path

The build checks for a local OrbPro file to catch missing dependencies early.
Default local file:

`../OrbPro/Build/OrbPro/OrbPro.esm.js`

Override with:

```bash
ORBPRO_ESM_PATH=/absolute/path/to/OrbPro.esm.js npm --prefix packages/spaceaware run build
```

## Notes

- `dist/index.html` is rewritten as an inlined single-file page.
- `dist/spaceaware.single.html` is a copy for explicit distribution.
- For IPFS/gateway deploys, run `npm run build:ipfs` and publish the full `dist/`
  folder so `dist/Build/*` is available under the same CID path.
