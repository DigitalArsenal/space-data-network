# SDN Svelte UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the SDN-branded upstream WebUI shell under `sdn-js/ui` with a native Svelte SDN product UI backed by a typed local/remote backend adapter contract and a fast Vite browser development loop.

**Architecture:** Keep `sdn-js/ui` as the built SDN UI path so desktop and server packaging continue to consume `sdn-js/ui/dist`. Move product behavior into Svelte components and a typed `SdnBackend` runtime contract, with `desktop-local` and `remote-sdn` adapters implemented first and `browser-node` represented as a degraded deferred adapter. Keep upstream IPFS WebUI separate under `/webui`.

**Tech Stack:** Svelte 5, Vite, TypeScript, Vitest, Playwright, Kubo RPC/gateway, SDN HTTP APIs, existing `sdn-js/src/ui/runtime` helpers.

---

## File Structure

- Modify `sdn-js/package.json`: add Svelte dependencies and UI scripts.
- Modify `sdn-js/ui/vite.config.mts`: add Svelte plugin, keep dev proxy support, keep build output at `sdn-js/ui/dist`.
- Modify `sdn-js/ui/src/main.ts`: mount the new Svelte app instead of `renderUpstreamWebUiBaseline`.
- Create `sdn-js/ui/src/App.svelte`: root Svelte app and route selection.
- Create `sdn-js/ui/src/styles/tokens.css`: Refero/Apple-inspired SDN design tokens.
- Create `sdn-js/ui/src/styles/app.css`: shell, card, button, table, drawer, and responsive styles.
- Create `sdn-js/ui/src/lib/backend-context.ts`: UI-side backend initialization and state bridge.
- Create `sdn-js/ui/src/lib/routes.ts`: hash/path route normalization and compatibility redirects.
- Create `sdn-js/ui/src/components/*.svelte`: shell, status, nav, cards, tables, drawers, and widgets.
- Create `sdn-js/ui/src/screens/*.svelte`: `NodeScreen`, `PeersScreen`, `LocalDataScreen`, `ClaimCoreScreen`.
- Create `sdn-js/src/ui/runtime/sdn-backend.ts`: typed backend contract and helpers.
- Create `sdn-js/src/ui/runtime/sdn-backend-desktop.ts`: desktop-local adapter.
- Create `sdn-js/src/ui/runtime/sdn-backend-remote.ts`: remote-sdn adapter.
- Create `sdn-js/src/ui/runtime/sdn-backend-browser.ts`: deferred browser-node adapter.
- Create tests beside runtime files and under `sdn-js/src/ui/sdn-ui/*.test.ts`.
- Modify `desktop/test/unit/dashboard.spec.js`: assert desktop still builds/copies `sdn-js/ui/dist` and routes `/sdn` to SDN UI.
- Modify `desktop/src/dashboard/index.js` only if route compatibility requires it; otherwise leave untouched.

## Task 1: Add Svelte Tooling And Keep Existing UI Build Path

**Files:**
- Modify: `sdn-js/package.json`
- Modify: `sdn-js/ui/vite.config.mts`
- Test: `sdn-js/src/ui/vite-config.test.ts`

- [x] **Step 1: Add a failing package script test**

Add this test to `sdn-js/src/ui/vite-config.test.ts`:

```ts
import fs from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

describe('sdn-ui package scripts', () => {
  it('keeps sdn-js/ui as the SDN UI build target and exposes a fast dev script', () => {
    const packageJson = JSON.parse(
      fs.readFileSync(path.resolve(__dirname, '../../package.json'), 'utf8'),
    ) as { scripts: Record<string, string> };

    expect(packageJson.scripts['build:ui']).toBe('vite build --config ui/vite.config.mts');
    expect(packageJson.scripts['build:sdn-ui']).toBe('vite build --config ui/vite.config.mts');
    expect(packageJson.scripts['dev:sdn-ui']).toBe('vite --config ui/vite.config.mts');
  });
});
```

- [x] **Step 2: Run the failing test**

Run:

```sh
npm --prefix sdn-js exec vitest run src/ui/vite-config.test.ts
```

Expected before implementation: fails because `build:sdn-ui` and `dev:sdn-ui` are missing.

- [x] **Step 3: Update `sdn-js/package.json` scripts and dependencies**

Add scripts:

```json
{
  "scripts": {
    "build:ui": "vite build --config ui/vite.config.mts",
    "build:sdn-ui": "vite build --config ui/vite.config.mts",
    "dev:sdn-ui": "vite --config ui/vite.config.mts",
    "check:sdn-ui": "svelte-check --tsconfig ui/tsconfig.json"
  }
}
```

Add Svelte build tooling as development dependencies only. Do not add
`lucide-svelte` until a UI task imports it, and do not ship `svelte` as a
runtime dependency while this package only compiles the UI during build.

```json
{
  "devDependencies": {
    "@sveltejs/vite-plugin-svelte": "^5.0.0",
    "svelte": "^5.0.0",
    "svelte-check": "^4.0.0"
  }
}
```

Run:

```sh
npm --prefix sdn-js install
```

- [x] **Step 4: Add Svelte to Vite config without changing output location**

In `sdn-js/ui/vite.config.mts`, import and add the Svelte plugin:

```ts
import { svelte } from '@sveltejs/vite-plugin-svelte';
```

Add it first in `plugins`:

```ts
plugins: [
  svelte(),
  // existing plugins stay below while upstream-webui compatibility files exist
]
```

Ensure `root: __dirname`, `base: './'`, and default output remain `sdn-js/ui/dist`.

- [x] **Step 5: Add `sdn-js/ui/tsconfig.json`**

Create:

```json
{
  "extends": "../tsconfig.json",
  "compilerOptions": {
    "allowJs": true,
    "checkJs": false,
    "noEmit": true,
    "rootDir": ".",
    "types": ["svelte"]
  },
  "include": ["src/**/*.svelte"],
  "exclude": ["dist", "node_modules"]
}
```

If no Svelte component exists yet, add a tiny hidden sentinel component under
`sdn-js/ui/src/` so `check:sdn-ui` proves Svelte tooling without traversing the
legacy upstream WebUI bridge. Do not include `src/**/*.ts` in this task; adapter
TypeScript checks are introduced after the SDN runtime contract exists.

- [x] **Step 6: Verify scripts and config**

Run:

```sh
npm --prefix sdn-js exec vitest run src/ui/vite-config.test.ts
npm --prefix sdn-js run check:sdn-ui
```

Expected: vitest passes; `svelte-check` passes with zero errors and does not
scan legacy upstream WebUI TypeScript.

- [x] **Step 7: Commit**

```sh
git add sdn-js/package.json sdn-js/package-lock.json sdn-js/ui/vite.config.mts sdn-js/ui/tsconfig.json sdn-js/src/ui/vite-config.test.ts
git commit -m "Add Svelte tooling for SDN UI"
```

## Task 2: Define The Typed `SdnBackend` Contract

**Files:**
- Create: `sdn-js/src/ui/runtime/sdn-backend.ts`
- Create: `sdn-js/src/ui/runtime/sdn-backend.test.ts`
- Modify: `sdn-js/src/ui/runtime/index.ts`

- [ ] **Step 1: Write the contract tests**

Create `sdn-js/src/ui/runtime/sdn-backend.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import {
  createCapability,
  createUnavailableResult,
  isBackendMode,
  normalizeBackendConfig,
} from './sdn-backend';

describe('SdnBackend contract helpers', () => {
  it('normalizes desktop-local configuration with Kubo and proxy URLs', () => {
    expect(normalizeBackendConfig({
      mode: 'desktop-local',
      kuboApiUrl: 'http://127.0.0.1:5001/',
      gatewayUrl: 'http://127.0.0.1:8081/',
      desktopProxyUrl: 'http://127.0.0.1:17890/',
    })).toEqual({
      mode: 'desktop-local',
      kuboApiUrl: 'http://127.0.0.1:5001',
      gatewayUrl: 'http://127.0.0.1:8081',
      desktopProxyUrl: 'http://127.0.0.1:17890',
      serverUrl: null,
    });
  });

  it('normalizes remote-sdn configuration with server URL only', () => {
    expect(normalizeBackendConfig({
      mode: 'remote-sdn',
      serverUrl: 'https://sdn.spaceaware.io/',
    })).toMatchObject({
      mode: 'remote-sdn',
      serverUrl: 'https://sdn.spaceaware.io',
    });
  });

  it('rejects unknown backend modes', () => {
    expect(isBackendMode('desktop-local')).toBe(true);
    expect(isBackendMode('remote-sdn')).toBe(true);
    expect(isBackendMode('browser-node')).toBe(true);
    expect(isBackendMode('webui')).toBe(false);
  });

  it('creates explicit degraded capability results', () => {
    expect(createCapability('runSqlQuery', 'degraded', 'local index unavailable')).toEqual({
      id: 'runSqlQuery',
      state: 'degraded',
      reason: 'local index unavailable',
    });
    expect(createUnavailableResult('exportCore', 'permission required')).toEqual({
      ok: false,
      capability: {
        id: 'exportCore',
        state: 'unavailable',
        reason: 'permission required',
      },
      data: null,
    });
  });
});
```

- [ ] **Step 2: Run the failing test**

Run:

```sh
npm --prefix sdn-js exec vitest run src/ui/runtime/sdn-backend.test.ts
```

Expected: fails because `sdn-backend.ts` does not exist.

- [ ] **Step 3: Create `sdn-backend.ts`**

Create:

```ts
export const BACKEND_MODES = ['desktop-local', 'remote-sdn', 'browser-node'] as const;

export type SdnBackendMode = (typeof BACKEND_MODES)[number];
export type CapabilityState =
  | 'available'
  | 'degraded'
  | 'unavailable'
  | 'permission-required'
  | 'remote-only'
  | 'local-only';

export interface BackendCapability {
  id: string;
  state: CapabilityState;
  reason?: string;
}

export interface BackendResult<T> {
  ok: boolean;
  capability: BackendCapability;
  data: T | null;
}

export interface SdnBackendConfig {
  mode: SdnBackendMode;
  kuboApiUrl: string | null;
  gatewayUrl: string | null;
  desktopProxyUrl: string | null;
  serverUrl: string | null;
}

export interface PartialSdnBackendConfig {
  mode?: string | null;
  kuboApiUrl?: string | null;
  gatewayUrl?: string | null;
  desktopProxyUrl?: string | null;
  serverUrl?: string | null;
}

export interface NodeSummary {
  displayName: string;
  peerId: string | null;
  agentVersion: string | null;
  online: boolean;
  runtime: SdnBackendMode;
}

export interface ObservedSdnPeer {
  id: string;
  name: string;
  addrs: string[];
  trustLevel: string;
  agentVersion?: string;
  protocols?: string[];
}

export interface StorageSummary {
  usedBytes: number | null;
  pinnedBytes: number | null;
  cacheBytes: number | null;
  quotaBytes: number | null;
}

export interface LocalObjectSummary {
  id: string;
  label: string;
  schema: string | null;
  source: string | null;
  sizeBytes: number | null;
  state: string;
  cid?: string;
}

export interface SdnBackend {
  readonly mode: SdnBackendMode;
  connect(): Promise<BackendResult<NodeSummary>>;
  getCapabilities(): Promise<BackendCapability[]>;
  getNodeSummary(): Promise<BackendResult<NodeSummary>>;
  getNodeProfile(): Promise<BackendResult<Record<string, unknown>>>;
  saveNodeProfile(profile: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>>;
  listObservedPeers(): Promise<BackendResult<ObservedSdnPeer[]>>;
  getStorageSummary(): Promise<BackendResult<StorageSummary>>;
  listObjects(): Promise<BackendResult<LocalObjectSummary[]>>;
  runSqlQuery(query: string): Promise<BackendResult<Array<Record<string, unknown>>>>;
  resolveCid(cid: string): Promise<BackendResult<{ cid: string; gatewayUrl: string }>>;
}

export function isBackendMode(value: string | null | undefined): value is SdnBackendMode {
  return BACKEND_MODES.includes(value as SdnBackendMode);
}

export function normalizeBackendConfig(input: PartialSdnBackendConfig): SdnBackendConfig {
  const mode = isBackendMode(input.mode) ? input.mode : 'desktop-local';
  return {
    mode,
    kuboApiUrl: trimTrailingSlash(input.kuboApiUrl) ?? 'http://127.0.0.1:5001',
    gatewayUrl: trimTrailingSlash(input.gatewayUrl) ?? 'http://127.0.0.1:8081',
    desktopProxyUrl: trimTrailingSlash(input.desktopProxyUrl) ?? null,
    serverUrl: trimTrailingSlash(input.serverUrl) ?? null,
  };
}

export function createCapability(id: string, state: CapabilityState, reason?: string): BackendCapability {
  return reason ? { id, state, reason } : { id, state };
}

export function createAvailableResult<T>(id: string, data: T): BackendResult<T> {
  return { ok: true, capability: createCapability(id, 'available'), data };
}

export function createUnavailableResult<T>(id: string, reason: string): BackendResult<T> {
  return { ok: false, capability: createCapability(id, 'unavailable', reason), data: null };
}

function trimTrailingSlash(value: string | null | undefined): string | null {
  const trimmed = value?.trim();
  if (!trimmed) return null;
  return trimmed.replace(/\/+$/, '');
}
```

- [ ] **Step 4: Export the contract**

Append to `sdn-js/src/ui/runtime/index.ts`:

```ts
export * from './sdn-backend';
```

- [ ] **Step 5: Verify**

Run:

```sh
npm --prefix sdn-js exec vitest run src/ui/runtime/sdn-backend.test.ts
```

Expected: passes.

- [ ] **Step 6: Commit**

```sh
git add sdn-js/src/ui/runtime/sdn-backend.ts sdn-js/src/ui/runtime/sdn-backend.test.ts sdn-js/src/ui/runtime/index.ts
git commit -m "Define SDN UI backend contract"
```

## Task 3: Implement `desktop-local`, `remote-sdn`, And Deferred `browser-node` Adapters

**Files:**
- Create: `sdn-js/src/ui/runtime/sdn-backend-desktop.ts`
- Create: `sdn-js/src/ui/runtime/sdn-backend-desktop.test.ts`
- Create: `sdn-js/src/ui/runtime/sdn-backend-remote.ts`
- Create: `sdn-js/src/ui/runtime/sdn-backend-remote.test.ts`
- Create: `sdn-js/src/ui/runtime/sdn-backend-browser.ts`
- Create: `sdn-js/src/ui/runtime/sdn-backend-factory.ts`
- Modify: `sdn-js/src/ui/runtime/index.ts`

- [ ] **Step 1: Write desktop adapter tests**

Create `sdn-js/src/ui/runtime/sdn-backend-desktop.test.ts` with fetch mocks:

```ts
import { describe, expect, it, vi } from 'vitest';
import { createDesktopLocalBackend } from './sdn-backend-desktop';

describe('desktop-local SDN backend', () => {
  it('loads node profile and observed SDN peers through local desktop routes', async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (url === 'http://127.0.0.1:17890/api/node/epm/json') {
        return jsonResponse({
          dn: 'Space Data Network Desktop',
          peer_id: '12D3KooWLocal',
          agent_version: 'kubo/0.39.0/sdn-desktop',
        });
      }
      if (url === 'http://127.0.0.1:17890/api/peers/sdn') {
        return jsonResponse([
          {
            id: '16Uiu2HAmReal',
            name: '16Uiu2HAmReal',
            addrs: ['/ip4/159.203.150.8/tcp/4001/p2p/16Uiu2HAmReal'],
            trust_level: 'observed',
            metadata: {
              agent_version: 'spacedatanetwork/1.0.3',
              protocols: '/space-data-network/module-delivery/1.0.0',
            },
          },
        ]);
      }
      throw new Error(`unexpected ${url}`);
    });

    const backend = createDesktopLocalBackend({
      desktopProxyUrl: 'http://127.0.0.1:17890',
      kuboApiUrl: 'http://127.0.0.1:5001',
      gatewayUrl: 'http://127.0.0.1:8081',
      fetch: fetchMock,
    });

    await expect(backend.getNodeProfile()).resolves.toMatchObject({
      ok: true,
      data: { peer_id: '12D3KooWLocal' },
    });
    await expect(backend.listObservedPeers()).resolves.toMatchObject({
      ok: true,
      data: [{ id: '16Uiu2HAmReal', trustLevel: 'observed' }],
    });
  });
});

function jsonResponse(payload: unknown) {
  return {
    ok: true,
    status: 200,
    json: async () => payload,
  } as Response;
}
```

- [ ] **Step 2: Write remote and browser adapter tests**

Create tests asserting:

```ts
expect(createRemoteSdnBackend({ serverUrl: 'https://sdn.spaceaware.io', fetch }).mode).toBe('remote-sdn');
expect(await createBrowserNodeBackend().getCapabilities()).toContainEqual({
  id: 'browser-node',
  state: 'degraded',
  reason: 'browser-node adapter is scheduled for Milestone 4',
});
```

- [ ] **Step 3: Run failing adapter tests**

Run:

```sh
npm --prefix sdn-js exec vitest run src/ui/runtime/sdn-backend-desktop.test.ts src/ui/runtime/sdn-backend-remote.test.ts
```

Expected: fails because adapters do not exist.

- [ ] **Step 4: Implement `createDesktopLocalBackend`**

Implement methods using `desktopProxyUrl` for `/api/*` routes, `kuboApiUrl` for Kubo calls, and `gatewayUrl` for CID resolution. Normalize peers into `ObservedSdnPeer` and split comma-separated `metadata.protocols`.

- [ ] **Step 5: Implement `createRemoteSdnBackend`**

Implement remote calls against `serverUrl`, starting with:

```text
GET /api/node/epm/json
GET /api/peers/sdn
GET /api/peers
GET /api/v1/data/objects
POST /api/v1/data/query
```

If an endpoint returns `404`, return a degraded capability result with the endpoint name in the reason.

- [ ] **Step 6: Implement `createBrowserNodeBackend`**

Return explicit degraded results for daemon-only methods and available capability metadata for local browser identity state. Do not claim browser-node is fully implemented.

- [ ] **Step 7: Implement `createSdnBackend` factory**

Create `sdn-js/src/ui/runtime/sdn-backend-factory.ts`:

```ts
import { normalizeBackendConfig, type PartialSdnBackendConfig, type SdnBackend } from './sdn-backend';
import { createBrowserNodeBackend } from './sdn-backend-browser';
import { createDesktopLocalBackend } from './sdn-backend-desktop';
import { createRemoteSdnBackend } from './sdn-backend-remote';

export function createSdnBackend(config: PartialSdnBackendConfig): SdnBackend {
  const normalized = normalizeBackendConfig(config);
  if (normalized.mode === 'remote-sdn') return createRemoteSdnBackend(normalized);
  if (normalized.mode === 'browser-node') return createBrowserNodeBackend(normalized);
  return createDesktopLocalBackend(normalized);
}
```

- [ ] **Step 8: Export adapters**

Append to `sdn-js/src/ui/runtime/index.ts`:

```ts
export * from './sdn-backend-browser';
export * from './sdn-backend-desktop';
export * from './sdn-backend-factory';
export * from './sdn-backend-remote';
```

- [ ] **Step 9: Verify**

Run:

```sh
npm --prefix sdn-js exec vitest run src/ui/runtime/sdn-backend*.test.ts
```

Expected: passes.

- [ ] **Step 10: Commit**

```sh
git add sdn-js/src/ui/runtime/sdn-backend*.ts
git commit -m "Add SDN UI backend adapters"
```

## Task 4: Create The Svelte App Shell And Design Tokens

**Files:**
- Modify: `sdn-js/ui/src/main.ts`
- Create: `sdn-js/ui/src/App.svelte`
- Create: `sdn-js/ui/src/styles/tokens.css`
- Create: `sdn-js/ui/src/styles/app.css`
- Create: `sdn-js/ui/src/lib/backend-context.ts`
- Create: `sdn-js/ui/src/lib/routes.ts`
- Create: `sdn-js/src/ui/sdn-ui/routes.test.ts`

- [ ] **Step 1: Write route compatibility tests**

Create `sdn-js/src/ui/sdn-ui/routes.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import { normalizeSdnRoute } from '../../../ui/src/lib/routes';

describe('SDN Svelte UI route compatibility', () => {
  it.each([
    ['/', '/node'],
    ['/status', '/node'],
    ['/settings', '/node?panel=advanced'],
    ['/files', '/local-data'],
    ['/pins', '/local-data?tab=pins'],
    ['/modules', '/peers?tab=modules'],
    ['/marketplace', '/peers?tab=marketplace'],
    ['/explore/bafy123', '/local-data?inspect=bafy123'],
  ])('maps %s to %s', (input, expected) => {
    expect(normalizeSdnRoute(input)).toBe(expected);
  });
});
```

- [ ] **Step 2: Run failing route test**

```sh
npm --prefix sdn-js exec vitest run src/ui/sdn-ui/routes.test.ts
```

Expected: fails because route helper does not exist.

- [ ] **Step 3: Implement `routes.ts`**

Create `sdn-js/ui/src/lib/routes.ts`:

```ts
export type PrimaryRoute = '/node' | '/peers' | '/local-data' | '/advanced' | '/claim-core';

export function normalizeSdnRoute(rawPath: string): string {
  const path = rawPath.startsWith('#') ? rawPath.slice(1) : rawPath;
  if (path === '' || path === '/' || path.startsWith('/status')) return '/node';
  if (path.startsWith('/settings')) return '/node?panel=advanced';
  if (path.startsWith('/files')) return '/local-data';
  if (path.startsWith('/pins')) return '/local-data?tab=pins';
  if (path.startsWith('/modules')) return '/peers?tab=modules';
  if (path.startsWith('/marketplace')) return '/peers?tab=marketplace';
  if (path.startsWith('/explore/')) return `/local-data?inspect=${encodeURIComponent(path.slice('/explore/'.length))}`;
  if (path.startsWith('/node') || path.startsWith('/peers') || path.startsWith('/local-data') || path.startsWith('/advanced') || path.startsWith('/claim-core')) return path;
  return '/node';
}
```

- [ ] **Step 4: Create design tokens**

Create `sdn-js/ui/src/styles/tokens.css` with:

```css
:root {
  color-scheme: dark;
  --sdn-bg: #050506;
  --sdn-bg-elevated: #0c0d10;
  --sdn-surface: #111318;
  --sdn-surface-2: #171a21;
  --sdn-border: rgba(255, 255, 255, 0.11);
  --sdn-border-strong: rgba(255, 255, 255, 0.18);
  --sdn-text: #f5f5f7;
  --sdn-text-muted: #a1a1aa;
  --sdn-text-subtle: #71717a;
  --sdn-blue: #0a84ff;
  --sdn-green: #30d158;
  --sdn-amber: #ffd60a;
  --sdn-red: #ff453a;
  --sdn-purple: #bf5af2;
  --sdn-radius: 12px;
  --sdn-radius-sm: 8px;
  --sdn-shadow: 0 20px 80px rgba(0, 0, 0, 0.35);
  --sdn-font: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "SF Pro Display", "Segoe UI", sans-serif;
}
```

- [ ] **Step 5: Create app shell styles**

Create `sdn-js/ui/src/styles/app.css` with stable layout classes:

```css
@import './tokens.css';

html,
body,
#root {
  min-height: 100%;
  margin: 0;
  background: var(--sdn-bg);
  color: var(--sdn-text);
  font-family: var(--sdn-font);
}

button,
input,
select,
textarea {
  font: inherit;
}

.sdn-app {
  min-height: 100vh;
  background:
    radial-gradient(circle at 20% 0%, rgba(10, 132, 255, 0.12), transparent 32rem),
    var(--sdn-bg);
}

.sdn-card {
  background: linear-gradient(180deg, rgba(255, 255, 255, 0.045), rgba(255, 255, 255, 0.02));
  border: 1px solid var(--sdn-border);
  border-radius: var(--sdn-radius);
}
```

- [ ] **Step 6: Create backend context**

Create `sdn-js/ui/src/lib/backend-context.ts` that reads query params and env vars:

```ts
import { createSdnBackend, type SdnBackend } from '../../../src/ui/runtime/sdn-backend-factory';

export function createBackendFromLocation(location: Location = window.location): SdnBackend {
  const params = new URLSearchParams(location.search);
  return createSdnBackend({
    mode: params.get('backend') ?? import.meta.env.SDN_UI_BACKEND ?? 'desktop-local',
    kuboApiUrl: params.get('api') ?? import.meta.env.SDN_UI_API_URL,
    gatewayUrl: params.get('gateway') ?? import.meta.env.SDN_UI_GATEWAY_URL,
    desktopProxyUrl: params.get('proxy') ?? import.meta.env.SDN_UI_PROXY_TARGET ?? location.origin,
    serverUrl: params.get('server') ?? import.meta.env.SDN_UI_SERVER_URL,
  });
}
```

- [ ] **Step 7: Replace `main.ts`**

Change `sdn-js/ui/src/main.ts` to:

```ts
import './styles/app.css';
import App from './App.svelte';

const target = document.getElementById('root');

if (!target) {
  throw new Error('SDN UI root element #root was not found');
}

const app = new App({ target });

export default app;
```

- [ ] **Step 8: Create the first `App.svelte` shell**

Create a minimal shell that renders the top navigation, active route, and backend mode using real route normalization. The shell must render `Node`, `Peers`, and `Local Data` labels and must not render upstream `Status` or `Files` nav items.

- [ ] **Step 9: Verify**

Run:

```sh
npm --prefix sdn-js exec vitest run src/ui/sdn-ui/routes.test.ts
npm --prefix sdn-js run check:sdn-ui
npm --prefix sdn-js run build:sdn-ui
```

Expected: passes and produces `sdn-js/ui/dist`.

- [ ] **Step 10: Commit**

```sh
git add sdn-js/ui/src sdn-js/src/ui/sdn-ui/routes.test.ts
git commit -m "Create Svelte SDN UI shell"
```

## Task 5: Build Node, Peers, And Local Data Screens From Backend Data

**Files:**
- Create: `sdn-js/ui/src/components/AppShell.svelte`
- Create: `sdn-js/ui/src/components/TopStatusBar.svelte`
- Create: `sdn-js/ui/src/components/SideNav.svelte`
- Create: `sdn-js/ui/src/components/StatusChip.svelte`
- Create: `sdn-js/ui/src/components/AdvancedDrawer.svelte`
- Create: `sdn-js/ui/src/components/cards/*.svelte`
- Create: `sdn-js/ui/src/screens/NodeScreen.svelte`
- Create: `sdn-js/ui/src/screens/PeersScreen.svelte`
- Create: `sdn-js/ui/src/screens/LocalDataScreen.svelte`
- Modify: `sdn-js/ui/src/App.svelte`
- Test: `sdn-js/src/ui/sdn-ui/app-shell.test.ts`

- [ ] **Step 1: Write DOM-render tests**

Use `@testing-library/svelte` if added, or use Svelte component instantiation in jsdom. Test:

```ts
expect(document.body.textContent).toContain('Node');
expect(document.body.textContent).toContain('Peers');
expect(document.body.textContent).toContain('Local Data');
expect(document.body.textContent).not.toContain('Status');
expect(document.body.textContent).not.toContain('Files');
```

- [ ] **Step 2: Implement shell components**

`SideNav.svelte` must render only:

```text
Node
Peers
Local Data
```

`TopStatusBar.svelte` must render backend mode, node state, peer count, wallet state, and storage summary from backend result state.

- [ ] **Step 3: Implement `NodeScreen.svelte`**

Use backend methods:

```ts
const profile = await backend.getNodeProfile();
const summary = await backend.getNodeSummary();
const capabilities = await backend.getCapabilities();
```

Render:

- Node Identity card.
- Runtime mode badge.
- Wallets & EPMs card with degraded state if unavailable.
- Identity inspector.
- Access & Roles card.
- Advanced drawer trigger for Kubo/IPFS diagnostics.

- [ ] **Step 4: Implement `PeersScreen.svelte`**

Use:

```ts
const peers = await backend.listObservedPeers();
```

Render:

- Search bar.
- Trusted/observed peer list.
- Data feeds, modules, and schemas tabs as explicit degraded cards until marketplace endpoints are wired.
- Mission loadout panel.

- [ ] **Step 5: Implement `LocalDataScreen.svelte`**

Use:

```ts
const storage = await backend.getStorageSummary();
const objects = await backend.listObjects();
```

Render:

- Storage summary.
- Pins and stored objects table.
- Object inspector.
- Rulesets.
- SQL workbench with degraded state if `runSqlQuery` is unavailable.

- [ ] **Step 6: Verify UI semantics**

Run:

```sh
npm --prefix sdn-js run check:sdn-ui
npm --prefix sdn-js run build:sdn-ui
npm --prefix sdn-js exec vitest run src/ui/sdn-ui
```

Expected: all pass.

- [ ] **Step 7: Commit**

```sh
git add sdn-js/ui/src sdn-js/src/ui/sdn-ui
git commit -m "Build SDN Svelte product screens"
```

## Task 6: Add Fast Vite Development Modes For Local And Remote Backends

**Files:**
- Modify: `sdn-js/ui/vite.config.mts`
- Create: `sdn-js/src/ui/sdn-ui/dev-config.test.ts`
- Modify: `sdn-js/README.md` or create `sdn-js/ui/README.md`

- [ ] **Step 1: Write dev config tests**

Assert Vite config proxies `/api`, `/ipfs`, and `/webui` only when `SDN_UI_PROXY_TARGET` is set, and exposes env vars through `import.meta.env`.

- [ ] **Step 2: Update Vite config**

Keep existing proxy structure and ensure:

```ts
const proxyTarget = process.env.SDN_UI_PROXY_TARGET?.trim();
```

Proxy:

```text
/api
/ipfs
/webui
```

Do not proxy `/webui` to the Svelte app. It remains upstream IPFS.

- [ ] **Step 3: Document local dev**

Add `sdn-js/ui/README.md`:

```md
# SDN UI Development

Local desktop/Kubo:

```sh
SDN_UI_BACKEND=desktop-local \
SDN_UI_API_URL=http://127.0.0.1:5001 \
SDN_UI_GATEWAY_URL=http://127.0.0.1:8081 \
SDN_UI_PROXY_TARGET=http://127.0.0.1:17890 \
npm --prefix sdn-js run dev:sdn-ui
```

Remote SDN:

```sh
SDN_UI_BACKEND=remote-sdn \
SDN_UI_SERVER_URL=https://sdn.spaceaware.io \
npm --prefix sdn-js run dev:sdn-ui
```
```

- [ ] **Step 4: Verify local dev server starts**

Run:

```sh
SDN_UI_BACKEND=desktop-local SDN_UI_PROXY_TARGET=http://127.0.0.1:17890 npm --prefix sdn-js run dev:sdn-ui -- --host 127.0.0.1 --port 5174
```

Expected: Vite prints a local URL. Stop the dev server after smoke verification.

- [ ] **Step 5: Commit**

```sh
git add sdn-js/ui/vite.config.mts sdn-js/ui/README.md sdn-js/src/ui/sdn-ui/dev-config.test.ts
git commit -m "Document SDN UI Vite development loop"
```

## Task 7: Wire Desktop And Server Hosting To The Svelte Build

**Files:**
- Modify: `desktop/test/unit/dashboard.spec.js`
- Modify: `desktop/package.json` only if script aliases need clarification.
- Inspect: `desktop/src/static-http-server.js`
- Inspect: `desktop/src/dashboard/index.js`
- Inspect/modify: `sdn-server` frontend serving code as needed.

- [ ] **Step 1: Write desktop guard tests**

In `desktop/test/unit/dashboard.spec.js`, assert:

```js
expect(packageJson.scripts['build:sdn-ui:build']).toBe('npm --prefix ../sdn-js run build:ui')
expect(packageJson.scripts['build:sdn-ui:copy']).toContain('../sdn-js/ui/dist')
expect(dashboardSource).toContain("registerStaticScheme({ scheme: 'sdn', directory: 'assets/sdn-ui' })")
```

- [ ] **Step 2: Run desktop guard tests**

```sh
npm --prefix desktop test -- --grep "SDN UI route|build:sdn-ui|uses SDN UI route"
```

Expected: passes or fails only on missing new assertions.

- [ ] **Step 3: Update server host if it points at old assumptions**

Find server references:

```sh
rg -n "sdn-js/ui|assets/sdn-ui|webui_path|frontend_path|admin_ui_path" sdn-server
```

If server already serves `sdn-js/ui/dist` or staged frontend assets, document no change. If it hard-codes old upstream WebUI paths, update it to use the built `sdn-js/ui/dist` artifact for `/` and keep `/webui` separate.

- [ ] **Step 4: Verify builds**

```sh
npm --prefix sdn-js run build:sdn-ui
npm --prefix desktop run build:sdn-ui
```

Expected: both pass and desktop copies Svelte output into `desktop/assets/sdn-ui`.

- [ ] **Step 5: Commit**

```sh
git add desktop/test/unit/dashboard.spec.js desktop/package.json sdn-server
git commit -m "Host Svelte SDN UI from desktop and server"
```

## Task 8: Browser Verification And Visual Guardrails

**Files:**
- Create: `sdn-js/playwright.sdn-ui.config.ts`
- Create: `sdn-js/tests/sdn-ui.spec.ts`
- Modify: `sdn-js/package.json`

- [ ] **Step 1: Add Playwright script**

Add:

```json
{
  "scripts": {
    "test:e2e:sdn-ui": "playwright test --config playwright.sdn-ui.config.ts"
  }
}
```

- [ ] **Step 2: Create Playwright tests**

Test:

- `/node` renders three nav items only.
- `/peers` shows SDN peers when `/api/peers/sdn` returns fixtures.
- `/local-data` shows storage and SQL degraded state.
- buttons do not use fully rounded bubble style except chips.
- no route sends the page to `/webui`.

- [ ] **Step 3: Run Vite and Playwright**

```sh
SDN_UI_BACKEND=desktop-local SDN_UI_PROXY_TARGET=http://127.0.0.1:17890 npm --prefix sdn-js run dev:sdn-ui -- --host 127.0.0.1 --port 5174
npm --prefix sdn-js run test:e2e:sdn-ui
```

Expected: Playwright passes. Stop Vite after tests.

- [ ] **Step 4: Capture screenshots**

Use Playwright screenshot assertions for:

```text
Node desktop
Peers desktop
Local Data desktop
Node mobile
Peers mobile
Local Data mobile
```

Store screenshots in test output, not committed artifacts.

- [ ] **Step 5: Commit**

```sh
git add sdn-js/package.json sdn-js/package-lock.json sdn-js/playwright.sdn-ui.config.ts sdn-js/tests/sdn-ui.spec.ts
git commit -m "Add SDN UI browser guardrails"
```

## Task 9: Desktop Package/Reinstall/Restart Verification

**Files:**
- No source edits unless verification exposes a bug.

- [ ] **Step 1: Package desktop**

```sh
npm --prefix desktop run package
```

Expected: exits 0. Known warnings about unsigned macOS app, dependency source maps, CSS `*zoom`, or chunk sizes are acceptable if unchanged.

- [ ] **Step 2: Reinstall local app**

```sh
osascript -e 'tell application "Space Data Network" to quit' >/dev/null 2>&1 || true
sleep 2
pkill -f 'Space Data Network.app' >/dev/null 2>&1 || true
rm -rf '/Applications/Space Data Network.app'
ditto 'desktop/dist/mac-universal/Space Data Network.app' '/Applications/Space Data Network.app'
open -a '/Applications/Space Data Network.app'
```

- [ ] **Step 3: Verify installed local endpoints**

```sh
curl -fsS http://127.0.0.1:17890/api/node/epm/json
curl -fsS http://127.0.0.1:17890/api/peers/sdn
```

Expected: both return HTTP 200.

- [ ] **Step 4: Verify installed SDN UI in browser**

Open:

```text
http://127.0.0.1:17890/sdn/?api=/ip4/127.0.0.1/tcp/5001&gateway=http%3A%2F%2F127.0.0.1%3A8081#/node
```

Verify:

- `Node`, `Peers`, `Local Data` nav is present.
- peer count is populated from `/api/peers/sdn`.
- Explore/CID inspection uses gateway `127.0.0.1:8081` when invoked.
- console has no page errors.

- [ ] **Step 5: Commit fixes if verification exposed bugs**

If verification required source edits:

```sh
git add <changed-files>
git commit -m "Fix SDN Svelte UI desktop packaging"
```

## Task 10: Final Integration, Push, And Stack Pin

**Files:**
- Component repo: `repos/main-packages/space-data-network`
- Stack repo: `/Users/tj/software/orbpro-stack`

- [ ] **Step 1: Run focused component verification**

```sh
npm --prefix sdn-js exec vitest run src/ui/runtime/sdn-backend*.test.ts src/ui/sdn-ui
npm --prefix sdn-js run check:sdn-ui
npm --prefix sdn-js run build:sdn-ui
npm --prefix desktop test -- --grep "SDN UI|SDN dashboard|uses SDN UI route"
```

- [ ] **Step 2: Check dirty files**

```sh
git -C repos/main-packages/space-data-network status --short --branch
```

Do not stage generated `desktop/assets/sdn-ui` or unrelated `sdn-server/bin` unless a task explicitly requires them.

- [ ] **Step 3: Push component branch**

```sh
git -C repos/main-packages/space-data-network push
```

- [ ] **Step 4: Update stack pin**

```sh
git add repos/main-packages/space-data-network
git commit -m "Update SDN Svelte UI pin"
git push
```

- [ ] **Step 5: Required stack verification**

```sh
git submodule status
git submodule foreach 'git status --short --branch'
```

Report unrelated dirty submodules separately. Do not revert them.

## Completion Criteria

- `sdn-js/ui` is a Svelte SDN product UI with `Node`, `Peers`, and `Local Data`.
- `/webui` remains upstream IPFS WebUI.
- Vite dev can run against local desktop/Kubo without desktop rebuilds.
- Vite dev can target remote SDN server configuration.
- `desktop-local` and `remote-sdn` adapters pass contract tests.
- `browser-node` exists as an explicit deferred degraded adapter.
- Desktop packaged app is reinstalled and restarted before claiming desktop-host completion.
- Component commit and stack pin are pushed.
