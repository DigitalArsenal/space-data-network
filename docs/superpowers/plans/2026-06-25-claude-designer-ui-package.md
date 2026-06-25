# Claude Designer UI Package Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a repeatable, uploadable Claude Designer handoff ZIP for redesigning the SDN Desktop/bundled UI.

**Architecture:** Keep production UI untouched. Add a tracked Designer package template under `design/claude-designer-ui-package/`, then add a Node generator that copies the template to `artifacts/design/claude-designer-ui-package/`, captures screenshots for each route, scans for forbidden content, and writes `artifacts/design/claude-designer-ui-package.zip`.

**Tech Stack:** Node.js ESM, `node:test`, Playwright from `sdn-js/node_modules`, static HTML/CSS/JS prototype, fixture JSON, system `zip` command.

---

## File Structure

- Create `scripts/build-claude-designer-ui-package.mjs`
  - Owns package generation, screenshot capture, forbidden-content scan, and ZIP creation.
- Create `scripts/build-claude-designer-ui-package.test.mjs`
  - Verifies the generator builds the package, expected files exist, screenshots exist, ZIP exists, and forbidden content is absent.
- Modify `package.json`
  - Add `build:designer-ui-package` script.
- Create `design/claude-designer-ui-package/CLAUDE_DESIGNER_BRIEF.md`
  - Designer-facing task prompt.
- Create `design/claude-designer-ui-package/SCREEN_INVENTORY.md`
  - Route-by-route workflow inventory.
- Create `design/claude-designer-ui-package/SOURCE_MAP.md`
  - Maps prototype surfaces to production Svelte/Desktop files.
- Create `design/claude-designer-ui-package/DESIGN_CONSTRAINTS.md`
  - Visual, UX, parity, and security constraints.
- Create `design/claude-designer-ui-package/IMPLEMENTATION_NOTES.md`
  - Explains how Claude Designer output should be translated back into production.
- Create `design/claude-designer-ui-package/prototype/index.html`
  - Static prototype shell.
- Create `design/claude-designer-ui-package/prototype/styles.css`
  - Static prototype styling.
- Create `design/claude-designer-ui-package/prototype/app.js`
  - Fixture-driven route switching and interactive states.
- Create `design/claude-designer-ui-package/prototype/data/fixtures.json`
  - Non-secret realistic sample data.

Generated files are not committed:

- `artifacts/design/claude-designer-ui-package/`
- `artifacts/design/claude-designer-ui-package.zip`

---

### Task 1: Write The Package Generator Test

**Files:**
- Create: `scripts/build-claude-designer-ui-package.test.mjs`

- [ ] **Step 1: Create the failing test**

Create `scripts/build-claude-designer-ui-package.test.mjs`:

```js
import { after, before, describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs';
import { readdir } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';

const repoRoot = resolve(new URL('..', import.meta.url).pathname);
const scriptPath = join(repoRoot, 'scripts/build-claude-designer-ui-package.mjs');
const tmpRoot = mkdtempSync(join(tmpdir(), 'sdn-designer-package-test-'));
const outputDir = join(tmpRoot, 'artifacts/design');
const packageDir = join(outputDir, 'claude-designer-ui-package');
const zipPath = join(outputDir, 'claude-designer-ui-package.zip');

function runGenerator() {
  return spawnSync(process.execPath, [
    scriptPath,
    '--output-dir',
    outputDir
  ], {
    cwd: repoRoot,
    encoding: 'utf8'
  });
}

function readPackageFile(relativePath) {
  return readFileSync(join(packageDir, relativePath), 'utf8');
}

async function listFiles(root, prefix = '') {
  const entries = await readdir(join(root, prefix), { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const relative = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (entry.isDirectory()) {
      files.push(...await listFiles(root, relative));
    } else {
      files.push(relative);
    }
  }
  return files.sort();
}

describe('Claude Designer UI package generator', () => {
  before(() => {
    rmSync(outputDir, { recursive: true, force: true });
  });

  after(() => {
    rmSync(tmpRoot, { recursive: true, force: true });
  });

  it('builds a self-contained handoff package with screenshots and ZIP archive', async () => {
    const result = runGenerator();
    assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`);

    const requiredFiles = [
      'CLAUDE_DESIGNER_BRIEF.md',
      'SCREEN_INVENTORY.md',
      'SOURCE_MAP.md',
      'DESIGN_CONSTRAINTS.md',
      'IMPLEMENTATION_NOTES.md',
      'prototype/index.html',
      'prototype/styles.css',
      'prototype/app.js',
      'prototype/data/fixtures.json',
      'screenshots/node.png',
      'screenshots/peers.png',
      'screenshots/data.png',
      'screenshots/channels.png',
      'screenshots/conjunction.png'
    ];

    for (const file of requiredFiles) {
      const absolute = join(packageDir, file);
      assert.equal(existsSync(absolute), true, `${file} should exist`);
      assert.ok(statSync(absolute).size > 0, `${file} should be non-empty`);
    }

    assert.equal(existsSync(zipPath), true, 'ZIP archive should exist');
    assert.ok(statSync(zipPath).size > 10_000, 'ZIP archive should contain prototype and screenshots');

    const indexHtml = readPackageFile('prototype/index.html');
    assert.match(indexHtml, /Space Data Network/);
    assert.match(indexHtml, /prototype\/data\/fixtures\.json/);

    const fixtures = JSON.parse(readPackageFile('prototype/data/fixtures.json'));
    assert.equal(fixtures.peers.some((peer) => peer.id === '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45'), true);
    assert.equal(fixtures.peers.some((peer) => peer.id === '16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3'), true);
    assert.deepEqual(fixtures.standards.map((standard) => standard.id), ['CAT', 'EPM', 'MPE', 'OMM', 'PNM', 'SPW']);

    const files = await listFiles(packageDir);
    assert.equal(files.some((file) => file.includes('node_modules')), false);
    assert.equal(files.some((file) => file.includes('.git')), false);

    const combinedText = files
      .filter((file) => /\.(md|html|css|js|json)$/.test(file))
      .map((file) => readPackageFile(file))
      .join('\n');
    assert.doesNotMatch(combinedText, /mnemonic|xpriv|private[_ -]?key|BEGIN [A-Z ]*PRIVATE KEY/i);
    assert.doesNotMatch(combinedText, /\/Users\/tj\/software\/orbpro-stack|\/Users\/tj\/\.config/i);
  });
});
```

- [ ] **Step 2: Run the test and verify it fails**

Run:

```bash
node --test scripts/build-claude-designer-ui-package.test.mjs
```

Expected result: failure because `scripts/build-claude-designer-ui-package.mjs` does not exist.

- [ ] **Step 3: Commit the failing test**

```bash
git add scripts/build-claude-designer-ui-package.test.mjs
git commit -m "test: cover Claude Designer UI package generator"
```

---

### Task 2: Add The Designer Handoff Template

**Files:**
- Create: `design/claude-designer-ui-package/CLAUDE_DESIGNER_BRIEF.md`
- Create: `design/claude-designer-ui-package/SCREEN_INVENTORY.md`
- Create: `design/claude-designer-ui-package/SOURCE_MAP.md`
- Create: `design/claude-designer-ui-package/DESIGN_CONSTRAINTS.md`
- Create: `design/claude-designer-ui-package/IMPLEMENTATION_NOTES.md`
- Create: `design/claude-designer-ui-package/prototype/data/fixtures.json`

- [ ] **Step 1: Add `CLAUDE_DESIGNER_BRIEF.md`**

Create `design/claude-designer-ui-package/CLAUDE_DESIGNER_BRIEF.md`:

```markdown
# Space Data Network UI Redesign Brief

You are redesigning the Space Data Network Desktop and bundled SDN UI.

The current UI is not acceptable. Keep the product capabilities, but redesign
the visual hierarchy, layout, navigation, controls, and state presentation.

Use this package as a design-mode prototype. It is intentionally standalone:
it does not call the SDN daemon, Kubo, Electron, or the live network.

## Product Context

Space Data Network is a peer-to-peer data network for space situational
awareness data. Users run a local node, manage identity, discover trusted
providers, query standards-based data, exchange encrypted data channels, and
screen private maneuver ephemeris for conjunction assessment.

## Required Top-Level Surfaces

- Node
- Peers
- Data
- Channels
- Conjunction

## Design Objective

Make the UI feel like a serious space-operations and network-console product.
It should be dense, scannable, composed, and useful for repeated operational
work. Do not turn it into a marketing landing page.

## Preserve These Product Ideas

- Local SDN node and service health
- Identity, EPM, vCard, and QR export
- Trusted and observed peer directory
- Provider and data-standard search
- Local and subscribed data workbench
- Encrypted channels, grants, and key envelopes
- Private maneuver ephemeris screening
- CLI/Desktop parity

## Freedom To Change

You may change layout, information hierarchy, navigation model, typography,
spacing, color, component style, and interaction grouping inside the prototype.

When a design change needs a backend change, call that out explicitly in your
notes rather than silently changing the product contract.
```

- [ ] **Step 2: Add `SCREEN_INVENTORY.md`**

Create `design/claude-designer-ui-package/SCREEN_INVENTORY.md`:

```markdown
# Screen Inventory

## Node

Primary job: show whether the local SDN node is usable and whether identity is
ready.

Content:
- node status and peer ID
- public and loopback addresses
- storage usage
- identity lock state
- EPM/vCard/QR export actions
- service lifecycle and update status

## Peers

Primary job: make provider discovery and trust state clear.

Content:
- observed and trusted peers
- SpaceAware and CelesTrak providers
- ownertrust
- peer identity metadata
- data feeds
- vCard and QR affordances

## Data

Primary job: let users find, sync, inspect, and query standards-based space
data.

Content:
- provider and data search
- standards: CAT, EPM, MPE, OMM, PNM, SPW
- schema sync state
- local FlatSQL store status
- query output modes: row/table, JSON, CSV
- row inspection

## Channels

Primary job: make encrypted exchange understandable and actionable.

Content:
- channel visibility and encryption state
- subscription state
- grant state
- recipient and key envelope controls
- stream publish/open actions
- monitor/detail pane

## Conjunction

Primary job: screen private maneuver ephemeris without revealing maneuver
intent to competitors.

Content:
- primary and secondary source selection
- grant and private channel inputs
- assessor peer and module version
- result channel
- table/JSON/CSV output modes
- provenance and encrypted workflow summary
```

- [ ] **Step 3: Add `SOURCE_MAP.md`**

Create `design/claude-designer-ui-package/SOURCE_MAP.md`:

```markdown
# Source Map

This package is a design handoff. Production implementation lives in the SDN
repo.

## Prototype To Production

| Prototype | Production |
| --- | --- |
| `prototype/index.html` | `sdn-js/ui/src/App.svelte` |
| App shell and nav | `sdn-js/ui/src/components/AppShell.svelte`, `sdn-js/ui/src/components/SideNav.svelte`, `sdn-js/ui/src/components/TopStatusBar.svelte` |
| Route normalization | `sdn-js/ui/src/lib/routes.ts` |
| Global style and tokens | `sdn-js/ui/src/styles/app.css`, `sdn-js/ui/src/styles/tokens.css` |
| Node screen | `sdn-js/ui/src/screens/NodeScreen.svelte` |
| Peers screen | `sdn-js/ui/src/screens/PeersScreen.svelte` |
| Data screen | `sdn-js/ui/src/screens/LocalDataScreen.svelte` |
| Channels screen | `sdn-js/ui/src/screens/ChannelsScreen.svelte` |
| Conjunction screen | `sdn-js/ui/src/screens/ConjunctionScreen.svelte` |
| Desktop route serving | `desktop/src/static-http-server.js`, `desktop/src/dashboard/index.js` |
| Packaged Desktop asset target | `desktop/assets/sdn-ui` |

## Boundary

The `webui/` directory is the upstream IPFS WebUI mirror. Do not treat it as
the main SDN redesign surface unless the redesign explicitly includes upstream
mirror overrides.
```

- [ ] **Step 4: Add `DESIGN_CONSTRAINTS.md`**

Create `design/claude-designer-ui-package/DESIGN_CONSTRAINTS.md`:

```markdown
# Design Constraints

- The app opens directly to product functionality, not a landing page.
- The UI should feel like a space-operations/network console.
- Keep information dense but organized.
- Use clear state language for online, degraded, locked, encrypted, trusted,
  stale, and unavailable states.
- Keep Desktop and CLI parity visible where workflows overlap.
- Make private maneuver ephemeris screening visible and understandable.
- Do not include secret-looking sample values.
- Do not imply that generic HTTP or SSH proxying is the product interface.
- Avoid decorative space gloss, orbs, oversized hero sections, and generic
  stock imagery.
- Keep controls familiar: tables, filters, segmented output modes, status
  chips, detail panes, copy/export buttons, and wizard-style identity actions.
```

- [ ] **Step 5: Add `IMPLEMENTATION_NOTES.md`**

Create `design/claude-designer-ui-package/IMPLEMENTATION_NOTES.md`:

```markdown
# Implementation Notes

Claude Designer output should be translated back into the Svelte app under
`sdn-js/ui/src`.

Recommended implementation order after design approval:

1. Extract reusable shell/navigation changes into `components/AppShell.svelte`,
   `components/SideNav.svelte`, and `components/TopStatusBar.svelte`.
2. Update CSS tokens in `styles/tokens.css`.
3. Update shared layout/component styles in `styles/app.css`.
4. Implement screen-level changes one route at a time.
5. Preserve backend calls and data contracts unless the approved design lists a
   backend implication.
6. Rebuild the SDN UI with `npm --prefix sdn-js run build:sdn-ui`.
7. Run focused SDN UI tests and Desktop route tests before packaging Desktop.

The prototype uses fixture data and does not define production API contracts.
```

- [ ] **Step 6: Add `fixtures.json`**

Create `design/claude-designer-ui-package/prototype/data/fixtures.json` with these top-level keys:

```json
{
  "node": {
    "name": "Local SDN Node",
    "peerId": "12D3KooWDesignerLocalNode",
    "status": "online",
    "mode": "desktop-local",
    "api": "http://127.0.0.1:5001",
    "gateway": "http://127.0.0.1:8080",
    "storage": "4.8 GB",
    "identity": {
      "state": "unlocked",
      "entity": "Space Data Network Operator",
      "epmCid": "bafkreidesignerpublicepmexample",
      "vcard": "Space Data Network Operator"
    },
    "service": {
      "state": "running",
      "autostart": true,
      "update": "0.47.0 current"
    }
  },
  "peers": [
    {
      "id": "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45",
      "name": "SpaceAware.io",
      "role": "Provider",
      "trust": "trusted",
      "addr": "/ip4/159.203.150.8/tcp/4001",
      "agent": "spacedatanetwork/1.0.3",
      "feeds": ["EPM", "MPE", "PNM"],
      "epmCid": "bafkreiggawraezbltnl3anwmabtuhvmlhdiotx5pxuqa7zmxkfjjjq35d4"
    },
    {
      "id": "16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3",
      "name": "CelesTrak Provider",
      "role": "Provider",
      "trust": "trusted",
      "addr": "/ip4/167.172.219.213/tcp/4001",
      "agent": "spacedatanetwork/1.0.3",
      "feeds": ["CAT", "OMM", "SPW"],
      "epmCid": "bafkreiekghfegduqfol5jemuagc7rpqnvfw5ilk67d5nybhred6ubfxwr4"
    }
  ],
  "standards": [
    { "id": "CAT", "label": "Satellite Catalog", "rows": 8462, "state": "synced" },
    { "id": "EPM", "label": "Entity Profile Metadata", "rows": 44, "state": "synced" },
    { "id": "MPE", "label": "Maneuver Ephemeris", "rows": 18, "state": "encrypted" },
    { "id": "OMM", "label": "Orbit Mean-Elements Message", "rows": 9120, "state": "synced" },
    { "id": "PNM", "label": "Provider Navigation Message", "rows": 236, "state": "synced" },
    { "id": "SPW", "label": "Space Weather", "rows": 96, "state": "fresh" }
  ],
  "channels": [
    {
      "id": "mpe-screening-alpha",
      "standard": "MPE",
      "visibility": "private",
      "subscription": "active",
      "grant": "granted",
      "encryption": "sealed",
      "recipient": "SpaceAware.io CA Assessor"
    },
    {
      "id": "provider-pnm-sync",
      "standard": "PNM",
      "visibility": "controlled",
      "subscription": "active",
      "grant": "not required",
      "encryption": "signed",
      "recipient": "Local SDN Node"
    }
  ],
  "conjunction": {
    "mode": "private-maneuver-ephemeris",
    "primary": "SpaceAware MPE grant",
    "secondary": "CelesTrak public catalog",
    "assessor": "SpaceAware.io CA Assessor",
    "module": "sdn-ca-screen/1.0.0",
    "resultChannel": "ca-results-private",
    "rows": [
      { "object": "SAT-44713", "tca": "2026-06-25T18:42:00Z", "missDistanceKm": 1.84, "pc": "2.1e-5", "state": "review" },
      { "object": "SAT-57944", "tca": "2026-06-26T03:10:00Z", "missDistanceKm": 8.92, "pc": "4.8e-7", "state": "clear" }
    ],
    "provenance": {
      "grant": "grant-mpe-alpha",
      "queryHash": "sha256:designerqueryexample",
      "resultHash": "sha256:designerresultexample"
    }
  }
}
```

- [ ] **Step 7: Run the test and verify it still fails**

Run:

```bash
node --test scripts/build-claude-designer-ui-package.test.mjs
```

Expected result: failure because the generator script still does not exist.

- [ ] **Step 8: Commit the handoff docs and fixtures**

```bash
git add design/claude-designer-ui-package
git commit -m "docs: add Claude Designer UI handoff template"
```

---

### Task 3: Add The Standalone Prototype

**Files:**
- Create: `design/claude-designer-ui-package/prototype/index.html`
- Create: `design/claude-designer-ui-package/prototype/styles.css`
- Create: `design/claude-designer-ui-package/prototype/app.js`

- [ ] **Step 1: Add `index.html`**

Create `design/claude-designer-ui-package/prototype/index.html`:

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Space Data Network UI Prototype</title>
    <link rel="stylesheet" href="./styles.css">
  </head>
  <body>
    <div class="app-shell">
      <aside class="side-nav" aria-label="Primary">
        <div class="brand">
          <div class="brand-mark" aria-hidden="true">SDN</div>
          <div>
            <strong>Space Data Network</strong>
            <span>Designer prototype</span>
          </div>
        </div>
        <nav class="nav-list">
          <button data-route="node" class="nav-item active" type="button">Node</button>
          <button data-route="peers" class="nav-item" type="button">Peers</button>
          <button data-route="data" class="nav-item" type="button">Data</button>
          <button data-route="channels" class="nav-item" type="button">Channels</button>
          <button data-route="conjunction" class="nav-item" type="button">Conjunction</button>
        </nav>
      </aside>

      <main class="workspace">
        <header class="top-bar">
          <div>
            <p class="eyebrow">Space operations network console</p>
            <h1 id="screen-title">Node</h1>
          </div>
          <div class="status-strip">
            <span class="status-chip good">online</span>
            <span class="status-chip">2 trusted peers</span>
            <span class="status-chip warn">identity unlocked</span>
          </div>
        </header>

        <section id="screen" class="screen" aria-live="polite"></section>
      </main>
    </div>

    <script type="module" src="./app.js"></script>
  </body>
</html>
```

- [ ] **Step 2: Add `styles.css`**

Create `design/claude-designer-ui-package/prototype/styles.css` with these selectors and no external imports:

```css
:root {
  color-scheme: dark;
  --bg: #080a0d;
  --panel: #10141a;
  --panel-2: #151b23;
  --line: #27313d;
  --text: #eef3f8;
  --muted: #9aa8b6;
  --subtle: #6f7d8c;
  --accent: #6ec6ff;
  --green: #69d58c;
  --amber: #f2c96d;
  --red: #ff7b72;
  --radius: 8px;
  font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
}

* { box-sizing: border-box; }

body {
  margin: 0;
  min-height: 100vh;
  background: var(--bg);
  color: var(--text);
}

button, input, select, textarea { font: inherit; }

.app-shell {
  display: grid;
  grid-template-columns: 240px minmax(0, 1fr);
  min-height: 100vh;
}

.side-nav {
  border-right: 1px solid var(--line);
  background: #05070a;
  padding: 24px 16px;
}

.brand {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 28px;
}

.brand-mark {
  display: grid;
  place-items: center;
  width: 42px;
  height: 42px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  color: var(--accent);
  font-weight: 700;
}

.brand strong, .brand span { display: block; }
.brand span { color: var(--muted); font-size: 12px; margin-top: 2px; }

.nav-list { display: grid; gap: 6px; }

.nav-item {
  width: 100%;
  border: 1px solid transparent;
  border-radius: var(--radius);
  background: transparent;
  color: var(--muted);
  padding: 10px 12px;
  text-align: left;
  cursor: pointer;
}

.nav-item:hover, .nav-item.active {
  background: var(--panel);
  border-color: var(--line);
  color: var(--text);
}

.workspace {
  display: grid;
  grid-template-rows: auto minmax(0, 1fr);
  min-width: 0;
}

.top-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 18px;
  border-bottom: 1px solid var(--line);
  background: #0b0f14;
  padding: 18px 28px;
}

.eyebrow {
  margin: 0 0 5px;
  color: var(--subtle);
  font-size: 12px;
  text-transform: uppercase;
  letter-spacing: 0;
}

h1, h2, h3, p { margin-top: 0; }
h1 { margin-bottom: 0; font-size: 24px; }
h2 { font-size: 18px; margin-bottom: 10px; }
h3 { font-size: 14px; margin-bottom: 8px; color: var(--muted); }

.status-strip, .actions, .segment, .filters {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}

.status-chip {
  display: inline-flex;
  align-items: center;
  min-height: 28px;
  border: 1px solid var(--line);
  border-radius: 999px;
  padding: 0 10px;
  color: var(--muted);
  background: var(--panel);
  font-size: 12px;
}

.status-chip.good { color: var(--green); }
.status-chip.warn { color: var(--amber); }
.status-chip.bad { color: var(--red); }

.screen {
  overflow: auto;
  padding: 24px 28px;
}

.grid {
  display: grid;
  grid-template-columns: repeat(12, minmax(0, 1fr));
  gap: 16px;
}

.panel {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
  padding: 16px;
}

.span-4 { grid-column: span 4; }
.span-5 { grid-column: span 5; }
.span-7 { grid-column: span 7; }
.span-8 { grid-column: span 8; }
.span-12 { grid-column: span 12; }

.metric {
  color: var(--text);
  font-size: 24px;
  font-weight: 700;
  margin-bottom: 4px;
}

.muted { color: var(--muted); }
.mono { font-family: ui-monospace, "SFMono-Regular", Menlo, Consolas, monospace; }

.table-wrap { overflow: auto; border: 1px solid var(--line); border-radius: var(--radius); }
table { width: 100%; border-collapse: collapse; }
th, td { border-bottom: 1px solid var(--line); padding: 10px 12px; text-align: left; vertical-align: top; }
th { color: var(--muted); font-size: 12px; font-weight: 600; }
tr[data-selectable] { cursor: pointer; }
tr[data-selectable]:hover, tr.selected { background: rgba(110, 198, 255, 0.08); }

.button {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel-2);
  color: var(--text);
  padding: 8px 10px;
  cursor: pointer;
}

.button.primary {
  border-color: rgba(110, 198, 255, 0.5);
  color: #061019;
  background: var(--accent);
}

.input, .select, textarea {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: #090d12;
  color: var(--text);
  padding: 8px 10px;
}

textarea { width: 100%; min-height: 260px; resize: vertical; }

.detail-list {
  display: grid;
  gap: 10px;
}

.detail-list div {
  display: grid;
  gap: 3px;
}

.detail-list dt {
  color: var(--subtle);
  font-size: 12px;
}

.detail-list dd {
  margin: 0;
  min-width: 0;
  overflow-wrap: anywhere;
}

@media (max-width: 900px) {
  .app-shell { grid-template-columns: 1fr; }
  .side-nav { border-right: 0; border-bottom: 1px solid var(--line); }
  .nav-list { grid-template-columns: repeat(5, minmax(0, 1fr)); }
  .top-bar { align-items: flex-start; flex-direction: column; }
  .span-4, .span-5, .span-7, .span-8 { grid-column: span 12; }
}
```

- [ ] **Step 3: Add `app.js`**

Create `design/claude-designer-ui-package/prototype/app.js` with route switching, selected peer/channel state, output mode state, and render functions. Use these exported screen names exactly so screenshot generation can address them:

```js
const routes = ['node', 'peers', 'data', 'channels', 'conjunction'];
const titles = {
  node: 'Node',
  peers: 'Peers',
  data: 'Data',
  channels: 'Channels',
  conjunction: 'Conjunction'
};

let fixtures = null;
let state = {
  route: normalizeRoute(location.hash),
  selectedPeerId: '',
  selectedChannelId: '',
  outputMode: 'table',
  dataQuery: ''
};

const screen = document.querySelector('#screen');
const screenTitle = document.querySelector('#screen-title');

async function init() {
  fixtures = await fetch('./data/fixtures.json').then((response) => response.json());
  state.selectedPeerId = fixtures.peers[0]?.id ?? '';
  state.selectedChannelId = fixtures.channels[0]?.id ?? '';
  document.querySelectorAll('[data-route]').forEach((button) => {
    button.addEventListener('click', () => navigate(button.dataset.route));
  });
  window.addEventListener('hashchange', () => {
    state.route = normalizeRoute(location.hash);
    render();
  });
  render();
}

function normalizeRoute(hash) {
  const route = String(hash || '#node').replace(/^#\/?/, '');
  return routes.includes(route) ? route : 'node';
}

function navigate(route) {
  location.hash = `#/${route}`;
}

function setOutputMode(mode) {
  state.outputMode = mode;
  render();
}

function render() {
  document.querySelectorAll('[data-route]').forEach((button) => {
    button.classList.toggle('active', button.dataset.route === state.route);
  });
  screenTitle.textContent = titles[state.route];
  screen.dataset.screen = state.route;
  screen.innerHTML = renderCurrentScreen();
  attachScreenHandlers();
}

function renderCurrentScreen() {
  if (state.route === 'peers') return renderPeers();
  if (state.route === 'data') return renderData();
  if (state.route === 'channels') return renderChannels();
  if (state.route === 'conjunction') return renderConjunction();
  return renderNode();
}

function renderNode() {
  const node = fixtures.node;
  return `
    <div class="grid">
      <section class="panel span-4">
        <h2>Node Health</h2>
        <div class="metric">${node.status}</div>
        <p class="muted">Mode: ${node.mode}</p>
        <dl class="detail-list">
          <div><dt>Peer ID</dt><dd class="mono">${node.peerId}</dd></div>
          <div><dt>API</dt><dd>${node.api}</dd></div>
          <div><dt>Gateway</dt><dd>${node.gateway}</dd></div>
        </dl>
      </section>
      <section class="panel span-4">
        <h2>Identity</h2>
        <div class="metric">${node.identity.state}</div>
        <p class="muted">${node.identity.entity}</p>
        <dl class="detail-list">
          <div><dt>EPM CID</dt><dd class="mono">${node.identity.epmCid}</dd></div>
          <div><dt>vCard</dt><dd>${node.identity.vcard}</dd></div>
        </dl>
        <div class="actions">
          <button class="button">JSON</button>
          <button class="button">CSV</button>
          <button class="button">vCard</button>
          <button class="button">QR</button>
        </div>
      </section>
      <section class="panel span-4">
        <h2>Service</h2>
        <div class="metric">${node.service.state}</div>
        <p class="muted">${node.service.update}</p>
        <dl class="detail-list">
          <div><dt>Autostart</dt><dd>${node.service.autostart ? 'enabled' : 'disabled'}</dd></div>
          <div><dt>Storage</dt><dd>${node.storage}</dd></div>
        </dl>
        <div class="actions">
          <button class="button primary">Restart</button>
          <button class="button">Stop</button>
          <button class="button">Check update</button>
        </div>
      </section>
    </div>
  `;
}

function renderPeers() {
  const selected = fixtures.peers.find((peer) => peer.id === state.selectedPeerId) ?? fixtures.peers[0];
  return `
    <div class="grid">
      <section class="panel span-8">
        <h2>Trusted And Observed Peers</h2>
        <div class="table-wrap">
          <table>
            <thead><tr><th>Name</th><th>Trust</th><th>Feeds</th><th>Address</th></tr></thead>
            <tbody>
              ${fixtures.peers.map((peer) => `
                <tr data-selectable data-peer-id="${peer.id}" class="${peer.id === selected.id ? 'selected' : ''}">
                  <td><strong>${peer.name}</strong><br><span class="muted mono">${peer.id}</span></td>
                  <td>${peer.trust}</td>
                  <td>${peer.feeds.join(', ')}</td>
                  <td class="mono">${peer.addr}</td>
                </tr>
              `).join('')}
            </tbody>
          </table>
        </div>
      </section>
      <section class="panel span-4">
        <h2>Provider Detail</h2>
        <dl class="detail-list">
          <div><dt>Name</dt><dd>${selected.name}</dd></div>
          <div><dt>Role</dt><dd>${selected.role}</dd></div>
          <div><dt>Agent</dt><dd>${selected.agent}</dd></div>
          <div><dt>EPM CID</dt><dd class="mono">${selected.epmCid}</dd></div>
        </dl>
        <div class="actions">
          <button class="button primary">Connect</button>
          <button class="button">vCard</button>
          <button class="button">QR</button>
        </div>
      </section>
    </div>
  `;
}

function renderData() {
  return `
    <div class="grid">
      <section class="panel span-12">
        <h2>Data Workbench</h2>
        <div class="filters">
          <input class="input" id="data-query" value="${state.dataQuery}" placeholder="Search providers, standards, schemas">
          <select class="select"><option>All providers</option><option>SpaceAware.io</option><option>CelesTrak Provider</option></select>
          <button class="button primary">Search</button>
        </div>
      </section>
      <section class="panel span-7">
        <h2>Standards</h2>
        <div class="table-wrap">
          <table>
            <thead><tr><th>Standard</th><th>Rows</th><th>State</th></tr></thead>
            <tbody>
              ${fixtures.standards.map((standard) => `
                <tr><td><strong>${standard.id}</strong><br><span class="muted">${standard.label}</span></td><td>${standard.rows.toLocaleString()}</td><td>${standard.state}</td></tr>
              `).join('')}
            </tbody>
          </table>
        </div>
      </section>
      <section class="panel span-5">
        <h2>Query Output</h2>
        ${renderOutputModeControls()}
        <textarea readonly>${renderDataOutput()}</textarea>
      </section>
    </div>
  `;
}

function renderChannels() {
  const selected = fixtures.channels.find((channel) => channel.id === state.selectedChannelId) ?? fixtures.channels[0];
  return `
    <div class="grid">
      <section class="panel span-7">
        <h2>Encrypted Channels</h2>
        <div class="table-wrap">
          <table>
            <thead><tr><th>Channel</th><th>Standard</th><th>Grant</th><th>Encryption</th></tr></thead>
            <tbody>
              ${fixtures.channels.map((channel) => `
                <tr data-selectable data-channel-id="${channel.id}" class="${channel.id === selected.id ? 'selected' : ''}">
                  <td>${channel.id}<br><span class="muted">${channel.recipient}</span></td>
                  <td>${channel.standard}</td>
                  <td>${channel.grant}</td>
                  <td>${channel.encryption}</td>
                </tr>
              `).join('')}
            </tbody>
          </table>
        </div>
      </section>
      <section class="panel span-5">
        <h2>Channel Monitor</h2>
        <dl class="detail-list">
          <div><dt>Selected channel</dt><dd>${selected.id}</dd></div>
          <div><dt>Visibility</dt><dd>${selected.visibility}</dd></div>
          <div><dt>Subscription</dt><dd>${selected.subscription}</dd></div>
          <div><dt>Recipient</dt><dd>${selected.recipient}</dd></div>
        </dl>
        <div class="actions">
          <button class="button primary">Open stream</button>
          <button class="button">Issue grant</button>
          <button class="button">Key envelope</button>
        </div>
      </section>
    </div>
  `;
}

function renderConjunction() {
  const result = fixtures.conjunction;
  return `
    <div class="grid">
      <section class="panel span-12">
        <h2>Private Maneuver Ephemeris Screening</h2>
        <p class="muted">Screen maneuvers without broadcasting maneuver intent to competitors.</p>
        <div class="filters">
          <select class="select"><option>${result.primary}</option></select>
          <select class="select"><option>${result.secondary}</option></select>
          <input class="input" value="${result.assessor}">
          <button class="button primary">Screen</button>
        </div>
      </section>
      <section class="panel span-7">
        <h2>Results</h2>
        ${renderOutputModeControls()}
        ${state.outputMode === 'table' ? renderConjunctionTable() : `<textarea readonly>${renderConjunctionOutput()}</textarea>`}
      </section>
      <section class="panel span-5">
        <h2>Provenance</h2>
        <dl class="detail-list">
          <div><dt>Mode</dt><dd>${result.mode}</dd></div>
          <div><dt>Module</dt><dd>${result.module}</dd></div>
          <div><dt>Result channel</dt><dd>${result.resultChannel}</dd></div>
          <div><dt>Grant</dt><dd>${result.provenance.grant}</dd></div>
          <div><dt>Query hash</dt><dd class="mono">${result.provenance.queryHash}</dd></div>
        </dl>
      </section>
    </div>
  `;
}

function renderOutputModeControls() {
  return `
    <div class="segment">
      ${['table', 'json', 'csv'].map((mode) => `<button class="button ${state.outputMode === mode ? 'primary' : ''}" data-output-mode="${mode}" type="button">${mode.toUpperCase()}</button>`).join('')}
    </div>
  `;
}

function renderDataOutput() {
  if (state.outputMode === 'json') return JSON.stringify(fixtures.standards, null, 2);
  if (state.outputMode === 'csv') return ['id,label,rows,state', ...fixtures.standards.map((standard) => `${standard.id},${standard.label},${standard.rows},${standard.state}`)].join('\n');
  return 'Select JSON or CSV to preview export data.';
}

function renderConjunctionTable() {
  return `
    <div class="table-wrap">
      <table>
        <thead><tr><th>Object</th><th>TCA</th><th>Miss km</th><th>Pc</th><th>State</th></tr></thead>
        <tbody>
          ${fixtures.conjunction.rows.map((row) => `<tr><td>${row.object}</td><td>${row.tca}</td><td>${row.missDistanceKm}</td><td>${row.pc}</td><td>${row.state}</td></tr>`).join('')}
        </tbody>
      </table>
    </div>
  `;
}

function renderConjunctionOutput() {
  if (state.outputMode === 'json') return JSON.stringify(fixtures.conjunction.rows, null, 2);
  if (state.outputMode === 'csv') return ['object,tca,missDistanceKm,pc,state', ...fixtures.conjunction.rows.map((row) => `${row.object},${row.tca},${row.missDistanceKm},${row.pc},${row.state}`)].join('\n');
  return '';
}

function attachScreenHandlers() {
  document.querySelectorAll('[data-peer-id]').forEach((row) => {
    row.addEventListener('click', () => {
      state.selectedPeerId = row.dataset.peerId;
      render();
    });
  });
  document.querySelectorAll('[data-channel-id]').forEach((row) => {
    row.addEventListener('click', () => {
      state.selectedChannelId = row.dataset.channelId;
      render();
    });
  });
  document.querySelectorAll('[data-output-mode]').forEach((button) => {
    button.addEventListener('click', () => setOutputMode(button.dataset.outputMode));
  });
  const dataQuery = document.querySelector('#data-query');
  if (dataQuery) {
    dataQuery.addEventListener('input', () => {
      state.dataQuery = dataQuery.value;
    });
  }
}

void init();
```

- [ ] **Step 4: Run the test and verify it still fails**

Run:

```bash
node --test scripts/build-claude-designer-ui-package.test.mjs
```

Expected result: failure because the generator script still does not exist.

- [ ] **Step 5: Commit the prototype**

```bash
git add design/claude-designer-ui-package/prototype
git commit -m "feat: add Claude Designer UI prototype"
```

---

### Task 4: Add The Package Generator

**Files:**
- Create: `scripts/build-claude-designer-ui-package.mjs`
- Modify: `package.json`

- [ ] **Step 1: Add the generator script**

Create `scripts/build-claude-designer-ui-package.mjs`:

```js
#!/usr/bin/env node

import { spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
import {
  cpSync,
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync
} from 'node:fs';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(__dirname, '..');
const templateDir = join(repoRoot, 'design/claude-designer-ui-package');
const args = process.argv.slice(2);
const outputDir = resolve(valueAfter('--output-dir') ?? join(repoRoot, 'artifacts/design'));
const packageDir = join(outputDir, 'claude-designer-ui-package');
const zipPath = join(outputDir, 'claude-designer-ui-package.zip');
const routes = ['node', 'peers', 'data', 'channels', 'conjunction'];

function valueAfter(flag) {
  const index = args.indexOf(flag);
  return index >= 0 ? args[index + 1] : null;
}

function log(message) {
  console.log(`[designer-package] ${message}`);
}

function copyTemplate() {
  if (!existsSync(templateDir)) {
    throw new Error(`missing template directory: ${templateDir}`);
  }
  rmSync(packageDir, { recursive: true, force: true });
  mkdirSync(outputDir, { recursive: true });
  cpSync(templateDir, packageDir, { recursive: true });
}

async function captureScreenshots() {
  const requireFromSdnJs = createRequire(join(repoRoot, 'sdn-js/package.json'));
  const { chromium } = requireFromSdnJs('playwright');
  const screenshotDir = join(packageDir, 'screenshots');
  mkdirSync(screenshotDir, { recursive: true });
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 960 }, deviceScaleFactor: 1 });
    const indexUrl = `file://${join(packageDir, 'prototype/index.html')}`;
    for (const route of routes) {
      await page.goto(`${indexUrl}#/${route}`);
      await page.waitForSelector(`[data-screen="${route}"]`, { timeout: 5000 });
      await page.screenshot({ path: join(screenshotDir, `${route}.png`), fullPage: true });
    }
  } finally {
    await browser.close();
  }
}

function listFiles(root, prefix = '') {
  const files = [];
  for (const entry of readdirSync(join(root, prefix), { withFileTypes: true })) {
    const relativePath = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (entry.isDirectory()) {
      files.push(...listFiles(root, relativePath));
    } else {
      files.push(relativePath);
    }
  }
  return files.sort();
}

function scanPackage() {
  const files = listFiles(packageDir);
  const required = [
    'CLAUDE_DESIGNER_BRIEF.md',
    'SCREEN_INVENTORY.md',
    'SOURCE_MAP.md',
    'DESIGN_CONSTRAINTS.md',
    'IMPLEMENTATION_NOTES.md',
    'prototype/index.html',
    'prototype/styles.css',
    'prototype/app.js',
    'prototype/data/fixtures.json',
    ...routes.map((route) => `screenshots/${route}.png`)
  ];
  for (const file of required) {
    const absolute = join(packageDir, file);
    if (!existsSync(absolute) || statSync(absolute).size === 0) {
      throw new Error(`required package file missing or empty: ${file}`);
    }
  }
  if (files.some((file) => file.includes('node_modules') || file.includes('.git'))) {
    throw new Error('package must not include node_modules or .git content');
  }
  const combinedText = files
    .filter((file) => /\.(md|html|css|js|json)$/.test(file))
    .map((file) => readFileSync(join(packageDir, file), 'utf8'))
    .join('\n');
  const forbidden = /mnemonic|xpriv|private[_ -]?key|BEGIN [A-Z ]*PRIVATE KEY|\/Users\/tj\/software\/orbpro-stack|\/Users\/tj\/\.config/i;
  if (forbidden.test(combinedText)) {
    throw new Error('package contains forbidden secret-like text or local absolute paths');
  }
}

function createZip() {
  rmSync(zipPath, { force: true });
  const result = spawnSync('zip', ['-qr', zipPath, 'claude-designer-ui-package'], {
    cwd: outputDir,
    encoding: 'utf8'
  });
  if (result.status !== 0) {
    throw new Error(`zip failed:\n${result.stdout}\n${result.stderr}`);
  }
}

async function main() {
  log(`copying template to ${relative(repoRoot, packageDir)}`);
  copyTemplate();
  log('capturing screenshots');
  await captureScreenshots();
  log('scanning package');
  scanPackage();
  log(`writing ${relative(repoRoot, zipPath)}`);
  createZip();
  log('done');
}

main().catch((error) => {
  console.error(error instanceof Error ? error.stack : error);
  process.exit(1);
});
```

- [ ] **Step 2: Add the package script**

Modify root `package.json` scripts:

```json
"build:designer-ui-package": "node scripts/build-claude-designer-ui-package.mjs"
```

Place it near the existing docs/build scripts.

- [ ] **Step 3: Run the generator test and verify it passes**

Run:

```bash
node --test scripts/build-claude-designer-ui-package.test.mjs
```

Expected result: PASS.

- [ ] **Step 4: Run the package script**

Run:

```bash
npm run build:designer-ui-package
```

Expected result:

```text
[designer-package] done
```

- [ ] **Step 5: Commit the generator**

```bash
git add package.json scripts/build-claude-designer-ui-package.mjs scripts/build-claude-designer-ui-package.test.mjs
git commit -m "feat: build Claude Designer UI package"
```

---

### Task 5: Verify The Upload Artifact

**Files:**
- Generated: `artifacts/design/claude-designer-ui-package/`
- Generated: `artifacts/design/claude-designer-ui-package.zip`

- [ ] **Step 1: Inspect ZIP contents**

Run:

```bash
zipinfo -1 artifacts/design/claude-designer-ui-package.zip | sed -n '1,120p'
```

Expected output includes:

```text
claude-designer-ui-package/CLAUDE_DESIGNER_BRIEF.md
claude-designer-ui-package/SCREEN_INVENTORY.md
claude-designer-ui-package/SOURCE_MAP.md
claude-designer-ui-package/DESIGN_CONSTRAINTS.md
claude-designer-ui-package/IMPLEMENTATION_NOTES.md
claude-designer-ui-package/prototype/index.html
claude-designer-ui-package/prototype/styles.css
claude-designer-ui-package/prototype/app.js
claude-designer-ui-package/prototype/data/fixtures.json
claude-designer-ui-package/screenshots/node.png
claude-designer-ui-package/screenshots/peers.png
claude-designer-ui-package/screenshots/data.png
claude-designer-ui-package/screenshots/channels.png
claude-designer-ui-package/screenshots/conjunction.png
```

- [ ] **Step 2: Serve the prototype locally**

Run:

```bash
python3 -m http.server 4181 --directory artifacts/design/claude-designer-ui-package/prototype
```

Expected result: server starts on `http://0.0.0.0:4181/`.

- [ ] **Step 3: Smoke the five routes**

In a second shell, run:

```bash
node -e "const routes=['node','peers','data','channels','conjunction']; for (const route of routes) console.log('http://127.0.0.1:4181/#/'+route)"
```

Open the routes or use Playwright:

```bash
npm --prefix sdn-js exec -- playwright screenshot http://127.0.0.1:4181/#/node /tmp/sdn-designer-node.png
```

Expected result: screenshot command exits 0 and image is non-empty.

- [ ] **Step 4: Stop the temporary server**

Stop the `python3 -m http.server` process with `Ctrl-C`.

- [ ] **Step 5: Final source verification**

Run:

```bash
node --test scripts/build-claude-designer-ui-package.test.mjs
npm run build:designer-ui-package
git diff --check
git status --short --branch
```

Expected:

- test passes;
- package script exits 0;
- `git diff --check` exits 0;
- `git status` shows only ignored/generated artifacts or a clean tree.

- [ ] **Step 6: Record the artifact path**

Final answer should include:

```text
artifacts/design/claude-designer-ui-package.zip
```

Also mention that the extracted prototype lives at:

```text
artifacts/design/claude-designer-ui-package/prototype/index.html
```
