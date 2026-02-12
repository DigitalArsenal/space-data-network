# `@spacedatanetwork/spaceaware`

Single-file SpaceAware beta homepage for `spaceaware.io`.

## What it includes

- Free-tier API console for:
  - `GET /api/v1/data/health`
  - `GET /api/v1/data/omm`
  - `GET /api/v1/data/mpe` (OMM-backed emission)
  - `GET /api/v1/data/cat`
- FlatBuffers-first querying (`format=flatbuffers` default)
- JSON debugging mode (`format=json`)
- Beta link sharing (deep-link query state)
- OrbPro globe loaded from an embedded single-file ESM bundle

## Build

From repo root:

```bash
npm --prefix packages/spaceaware run build
```

Outputs:

- `packages/spaceaware/dist/index.html`
- `packages/spaceaware/dist/spaceaware.single.html`

Both are single-file HTML artifacts with OrbPro embedded inline.

### OrbPro input path

Default OrbPro bundle path:

`../OrbPro/Build/OrbPro/OrbPro.esm.js`

Override with:

```bash
ORBPRO_ESM_PATH=/absolute/path/to/OrbPro.esm.js npm --prefix packages/spaceaware run build
```

## Local serve

```bash
npm --prefix packages/spaceaware run serve
```
