# SDN CLI Desktop UI Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the first SDN release expose the same install, identity, provider discovery, data search/query, encrypted CA/MPE, lifecycle, update, remove, release, and documentation behavior across CLI, Desktop, and bundled UI.

**Architecture:** Treat the SDN daemon as the authoritative backend contract, make Desktop either proxy or locally implement the same route contract, and keep CLI/UI behavior checked against a shared parity contract. Work lands in bounded slices so each slice has focused tests and can be committed before the next subsystem changes.

**Tech Stack:** Go/Cobra CLI, Electron Desktop static HTTP server, Node test runner, Playwright unit tests, Svelte/TypeScript SDN UI runtime, GitHub Actions release workflows, IPFS/Kubo/libp2p live DHT.

---

## Scope Split

The design covers multiple independent subsystems. This plan is the master
execution plan for the full objective and gives the first slice enough detail
to execute immediately. Each remaining slice has named files, tests, commands,
and commits; expand a slice with code-level TDD steps immediately before its
first code edit, then execute that expanded slice without changing the scope.

## File Responsibilities

- `docs/superpowers/specs/2026-06-24-cli-desktop-ui-parity-design.md`
  records the product parity contract and acceptance criteria.
- `docs/superpowers/plans/2026-06-24-sdn-cli-desktop-ui-parity.md`
  tracks executable implementation tasks and verification evidence.
- `desktop/src/static-http-server.js`
  owns Desktop-local SDN API route parity for bundled UI pages.
- `desktop/test/unit/static-http-server-identity.spec.js`
  owns Desktop identity, EPM, vCard, peer artifact, and auth route tests.
- `desktop/test/unit/static-http-server-data.spec.js`
  will own Desktop data/search route tests.
- `desktop/src/app-menu.js` and `desktop/src/tray.js`
  own Desktop Help/About menu links.
- `desktop/test/unit/app-menu.spec.js` and
  `desktop/test/unit/tray-sdn-links.spec.js`
  will own Desktop product-link assertions.
- `sdn-server/cmd/spacedatanetwork/search_cli.go` and related tests own CLI
  search/provider output and live/API mode behavior.
- `sdn-server/cmd/spacedatanetwork/providers_cli.go` will own requester-facing
  provider commands.
- `sdn-server/cmd/spacedatanetwork/conjunction_cli.go` will own CLI encrypted
  CA/MPE workflow entry points.
- `sdn-js/src/ui/runtime/sdn-backend-desktop.ts` and runtime tests own UI
  calls into Desktop route contracts.
- `.github/workflows/live-dht-cross-platform.yml` and
  `deployment/release/live-dht-*.mjs` own cross-platform live-DHT verification.
- `deployment/release/*` release tests own installers, artifact assembly,
  updater payloads, release body links, and docs parity checks.

---

## Slice 1: Parity Contract, Desktop Route Parity, And Desktop Help Links

### Task 1: Add A Machine-Readable Parity Contract

**Files:**
- Create: `deployment/release/sdn-parity-contract.json`
- Create: `deployment/release/sdn-parity-contract.test.mjs`
- Modify: `docs/superpowers/specs/2026-06-24-cli-desktop-ui-parity-design.md`

- [x] **Step 1: Write the failing parity contract test**

Create `deployment/release/sdn-parity-contract.test.mjs`:

```js
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

const contract = JSON.parse(readFileSync(new URL('./sdn-parity-contract.json', import.meta.url), 'utf8'));

test('SDN parity contract covers first-version product surfaces', () => {
  assert.equal(contract.version, 1);
  assert.deepEqual(contract.surfaces, ['cli', 'desktop', 'ui', 'release', 'docs']);

  const ids = new Set(contract.capabilities.map((capability) => capability.id));
  for (const required of [
    'install.user_scoped',
    'identity.bootstrap',
    'identity.export',
    'identity.directory',
    'desktop.route.node_epm_vcard',
    'desktop.route.peer_epm',
    'desktop.route.auth_user_update',
    'data.search',
    'data.query',
    'provider.search',
    'provider.interaction',
    'encrypted_ca.maneuver_ephemeris',
    'lifecycle.service',
    'lifecycle.remove',
    'update.daemon_in_place',
    'release.desktop_artifacts',
    'ci.live_dht_cross_platform'
  ]) {
    assert.ok(ids.has(required), `missing parity capability ${required}`);
  }
});

test('every parity capability names surfaces and tests', () => {
  for (const capability of contract.capabilities) {
    assert.equal(typeof capability.id, 'string');
    assert.ok(capability.id.length > 0);
    assert.ok(Array.isArray(capability.surfaces), `${capability.id} surfaces must be an array`);
    assert.ok(capability.surfaces.length > 0, `${capability.id} must name at least one surface`);
    assert.ok(Array.isArray(capability.tests), `${capability.id} tests must be an array`);
    assert.ok(capability.tests.length > 0, `${capability.id} must name at least one proving test`);
  }
});
```

- [x] **Step 2: Run the contract test and verify it fails**

Run:

```bash
node --test deployment/release/sdn-parity-contract.test.mjs
```

Expected: FAIL because `deployment/release/sdn-parity-contract.json` does not exist.

- [x] **Step 3: Add the initial parity contract**

Create `deployment/release/sdn-parity-contract.json`:

```json
{
  "version": 1,
  "surfaces": ["cli", "desktop", "ui", "release", "docs"],
  "capabilities": [
    {
      "id": "install.user_scoped",
      "surfaces": ["cli", "release", "docs"],
      "tests": ["deployment/release/install-script.test.mjs"]
    },
    {
      "id": "identity.bootstrap",
      "surfaces": ["cli", "desktop", "ui"],
      "tests": [
        "sdn-server/cmd/spacedatanetwork/main_test.go",
        "desktop/test/unit/static-http-server-identity.spec.js",
        "sdn-js/src/ui/runtime/identity.test.ts"
      ]
    },
    {
      "id": "identity.export",
      "surfaces": ["cli", "desktop", "ui"],
      "tests": [
        "sdn-server/cmd/spacedatanetwork/main_test.go",
        "desktop/test/unit/static-http-server-identity.spec.js"
      ]
    },
    {
      "id": "identity.directory",
      "surfaces": ["cli", "desktop", "ui"],
      "tests": [
        "sdn-server/cmd/spacedatanetwork/identity_wizard_cli_test.go",
        "desktop/test/unit/static-http-server-identity.spec.js",
        "sdn-js/src/ui/runtime/directory.test.ts"
      ]
    },
    {
      "id": "desktop.route.node_epm_vcard",
      "surfaces": ["desktop", "ui"],
      "tests": ["desktop/test/unit/static-http-server-identity.spec.js"]
    },
    {
      "id": "desktop.route.peer_epm",
      "surfaces": ["desktop", "ui"],
      "tests": ["desktop/test/unit/static-http-server-identity.spec.js"]
    },
    {
      "id": "desktop.route.auth_user_update",
      "surfaces": ["desktop", "ui"],
      "tests": ["desktop/test/unit/static-http-server-identity.spec.js"]
    },
    {
      "id": "data.search",
      "surfaces": ["cli", "desktop", "ui"],
      "tests": [
        "sdn-server/cmd/spacedatanetwork/search_cli_test.go",
        "desktop/test/unit/static-http-server-data.spec.js",
        "sdn-js/src/ui/runtime/sdn-backend-desktop.test.ts"
      ]
    },
    {
      "id": "data.query",
      "surfaces": ["cli", "desktop", "ui"],
      "tests": [
        "deployment/release/test-release-artifacts-docker.test.mjs",
        "desktop/test/unit/static-http-server-data.spec.js",
        "sdn-js/src/ui/runtime/sdn-backend-desktop.test.ts"
      ]
    },
    {
      "id": "provider.search",
      "surfaces": ["cli", "desktop", "ui"],
      "tests": [
        "sdn-server/cmd/spacedatanetwork/search_cli_test.go",
        "desktop/test/unit/static-http-server-data.spec.js",
        "sdn-js/src/ui/runtime/sdn-backend-desktop.test.ts"
      ]
    },
    {
      "id": "provider.interaction",
      "surfaces": ["cli", "desktop", "ui"],
      "tests": [
        "sdn-server/cmd/spacedatanetwork/providers_cli_test.go",
        "sdn-js/src/ui/runtime/sdn-backend-desktop.test.ts"
      ]
    },
    {
      "id": "encrypted_ca.maneuver_ephemeris",
      "surfaces": ["cli", "desktop", "ui"],
      "tests": [
        "sdn-server/cmd/spacedatanetwork/conjunction_cli_test.go",
        "sdn-js/tests/sdn-ui.spec.ts"
      ]
    },
    {
      "id": "lifecycle.service",
      "surfaces": ["cli", "desktop"],
      "tests": [
        "sdn-server/cmd/spacedatanetwork/service_test.go",
        "desktop/test/unit/tray-service.spec.js"
      ]
    },
    {
      "id": "lifecycle.remove",
      "surfaces": ["cli", "desktop", "docs"],
      "tests": [
        "sdn-server/cmd/spacedatanetwork/service_test.go",
        "deployment/release/install-script.test.mjs"
      ]
    },
    {
      "id": "update.daemon_in_place",
      "surfaces": ["cli", "desktop", "release"],
      "tests": [
        "sdn-server/cmd/spacedatanetwork/update_cli_test.go",
        "desktop/test/unit/sdn-updater-staged-install.spec.js",
        "deployment/release/build-cli-update-payload.test.mjs"
      ]
    },
    {
      "id": "release.desktop_artifacts",
      "surfaces": ["desktop", "release", "docs"],
      "tests": [
        "deployment/release/beta-release-workflow.test.mjs",
        "deployment/release/assemble-beta-release-artifacts.test.mjs",
        "deployment/release/beta-download-links.test.mjs"
      ]
    },
    {
      "id": "ci.live_dht_cross_platform",
      "surfaces": ["cli", "desktop", "release"],
      "tests": [
        "deployment/release/live-dht-workflow.test.mjs",
        "deployment/release/live-dht-client-smoke.test.mjs"
      ]
    }
  ]
}
```

- [x] **Step 4: Link the design spec to the contract file**

In `docs/superpowers/specs/2026-06-24-cli-desktop-ui-parity-design.md`,
replace the first sentence under `### Shared Capability Registry` with:

```markdown
The machine-readable contract lives at
`deployment/release/sdn-parity-contract.json` and is checked by
`deployment/release/sdn-parity-contract.test.mjs`.
```

- [x] **Step 5: Run the contract test and verify it passes**

Run:

```bash
node --test deployment/release/sdn-parity-contract.test.mjs
```

Expected: PASS.

- [x] **Step 6: Commit the contract**

Run:

```bash
git add deployment/release/sdn-parity-contract.json deployment/release/sdn-parity-contract.test.mjs docs/superpowers/specs/2026-06-24-cli-desktop-ui-parity-design.md
git commit -m "test: add SDN parity contract"
```

2026-06-24 update: the parity contract was expanded after the initial contract
commit to map objective requirements `R01` through `R13`, require acceptance
checks per capability, and encode exact search modes, installer commands,
update provider, Desktop release platforms, live-DHT five-minute registration
wait, and cross-platform proof requirements. The update was driven by a
red/green extension to `deployment/release/sdn-parity-contract.test.mjs`.

### Task 2: Add Desktop Node vCard Route Coverage

**Files:**
- Modify: `desktop/test/unit/static-http-server-identity.spec.js`
- Modify: `desktop/src/static-http-server.js`

- [x] **Step 1: Add a failing node vCard test**

In the existing `serves the local node EPM route as a raw FlatBuffer` test,
after the `expect(epm.keysLength()).toBe(2)` assertions, add:

```js
    const vcard = await requestRaw(serveDesktopNodeEPMAPI, 'GET', '/api/node/epm/vcard')
    expect(vcard.statusCode).toBe(200)
    expect(vcard.headers['Content-Type']).toContain('text/vcard')
    expect(vcard.body).toContain('BEGIN:VCARD')
    expect(vcard.body).toContain('FN:Desktop Node')
    expect(vcard.body).toContain('EMAIL;TYPE=INTERNET:node@example.invalid')
    expect(vcard.body).toContain('X-SDN-PEER-ID:12D3KooWDesktopNode')
    expect(vcard.body).toContain('X-SDN-SIGNING-PUBLIC-KEY:ed25519-node-signing-public')
    expect(vcard.body).toContain('X-SDN-ENCRYPTION-PUBLIC-KEY:x25519-node-encryption-public')
```

- [x] **Step 2: Run the identity test and verify it fails**

Run:

```bash
npx playwright test desktop/test/unit/static-http-server-identity.spec.js -g "serves the local node EPM route as a raw FlatBuffer"
```

Expected: FAIL because `/api/node/epm/vcard` returns 404 or method not allowed.

- [x] **Step 3: Implement `/api/node/epm/vcard`**

In `desktop/src/static-http-server.js`, update the route guard at the start of
`serveDesktopNodeEPMAPI` to include `/api/node/epm/vcard`, and add this handler
after the `/api/node/epm/json` GET handler:

```js
  if (req.method === 'GET' && parsed.pathname === '/api/node/epm/vcard') {
    res.writeHead(200, staticAssetHeaders('text/vcard; charset=utf-8'))
    res.end(epmProfileToVCard(await readDesktopNodeProfile()))
    return true
  }
```

- [x] **Step 4: Run the focused identity test and verify it passes**

Run:

```bash
npx playwright test desktop/test/unit/static-http-server-identity.spec.js -g "serves the local node EPM route as a raw FlatBuffer"
```

Expected: PASS.

### Task 3: Add Desktop Peer EPM And vCard Routes

**Files:**
- Modify: `desktop/test/unit/static-http-server-identity.spec.js`
- Modify: `desktop/src/static-http-server.js`

- [x] **Step 1: Add failing peer artifact route tests**

Add this test to `desktop/test/unit/static-http-server-identity.spec.js`:

```js
  test('serves peer EPM and vCard artifacts from hosted directory records', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-peer-epm-api-'))
    const { serveDesktopIdentityAPI, serveDesktopPeerEPMAPI } = loadStaticServer(userData)

    await requestJson(serveDesktopIdentityAPI, 'PUT', '/api/identity/epms/provider', {
      epm_json: {
        dn: 'Provider Node',
        email: 'provider@example.invalid',
        entity_type: 'Node',
        peer_id: '16Uiu2ProviderPeer',
        public_key: 'provider-public',
        signing_public_key: 'provider-signing',
        encryption_public_key: 'provider-encryption'
      }
    })

    const epm = await requestRaw(serveDesktopPeerEPMAPI, 'GET', '/api/peers/16Uiu2ProviderPeer/epm')
    expect(epm.statusCode).toBe(200)
    expect(epm.headers['Content-Type']).toBe('application/x-flatbuffers')

    const vcard = await requestRaw(serveDesktopPeerEPMAPI, 'GET', '/api/peers/16Uiu2ProviderPeer/epm/vcard')
    expect(vcard.statusCode).toBe(200)
    expect(vcard.headers['Content-Type']).toContain('text/vcard')
    expect(vcard.body).toContain('FN:Provider Node')
    expect(vcard.body).toContain('X-SDN-PEER-ID:16Uiu2ProviderPeer')

    const missing = await requestRaw(serveDesktopPeerEPMAPI, 'GET', '/api/peers/16Uiu2Missing/epm')
    expect(missing.statusCode).toBe(404)
  })
```

- [x] **Step 2: Run the peer route test and verify it fails**

Run:

```bash
npx playwright test desktop/test/unit/static-http-server-identity.spec.js -g "serves peer EPM and vCard artifacts"
```

Expected: FAIL because `serveDesktopPeerEPMAPI` is not exported.

- [x] **Step 3: Implement `serveDesktopPeerEPMAPI`**

Add this function in `desktop/src/static-http-server.js` near the other
identity route handlers:

```js
async function serveDesktopPeerEPMAPI (req, res) {
  const parsed = new URL(req.url || '/', `http://${HOST}`)
  const match = parsed.pathname.match(/^\/api\/peers\/([^/]+)\/epm(?:\/vcard)?$/)
  if (!match) return false

  if (req.method !== 'GET') {
    sendJSON(res, 405, { error: 'method not allowed' })
    return true
  }

  const peerId = decodeURIComponent(match[1])
  const records = await readHostedEpmRecords()
  const record = records.find(candidate => readEpmString(candidate.epm_json, ['peer_id', 'peerId']) === peerId)
  if (!record) {
    sendJSON(res, 404, { error: 'peer EPM not found' })
    return true
  }

  const profile = publicEpmProfile(record.epm_json)
  if (parsed.pathname.endsWith('/vcard')) {
    res.writeHead(200, staticAssetHeaders('text/vcard; charset=utf-8'))
    res.end(epmProfileToVCard(profile))
    return true
  }

  sendBuffer(res, 200, 'application/x-flatbuffers', await buildDesktopNodeEPM(profile))
  return true
}
```

Add the route before `serveDesktopNodeEPMAPI` in the static server chain:

```js
        .then(handled => handled || serveDesktopPeerEPMAPI(req, res))
```

Export `serveDesktopPeerEPMAPI` from `module.exports`.

- [x] **Step 4: Run the peer route test and verify it passes**

Run:

```bash
npx playwright test desktop/test/unit/static-http-server-identity.spec.js -g "serves peer EPM and vCard artifacts"
```

Expected: PASS.

### Task 4: Add Desktop Auth User Update Route

**Files:**
- Modify: `desktop/test/unit/static-http-server-identity.spec.js`
- Modify: `desktop/src/static-http-server.js`

- [x] **Step 1: Add failing auth user route tests**

Add this test to `desktop/test/unit/static-http-server-identity.spec.js`:

```js
  test('accepts local desktop auth user create and update routes used by settings override', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-auth-users-api-'))
    const { serveDesktopAuthUsersAPI } = loadStaticServer(userData)

    const grant = {
      xpub: 'xpub-desktop-admin',
      label: 'Desktop Admin',
      role: 'admin',
      trust_level: 'local'
    }

    const created = await requestJson(serveDesktopAuthUsersAPI, 'POST', '/api/auth/users', grant)
    expect(created.statusCode).toBe(200)
    expect(created.json.user).toMatchObject(grant)

    const conflict = await requestJson(serveDesktopAuthUsersAPI, 'POST', '/api/auth/users', grant)
    expect(conflict.statusCode).toBe(409)

    const updated = await requestJson(serveDesktopAuthUsersAPI, 'PUT', '/api/auth/users/xpub-desktop-admin', {
      ...grant,
      label: 'Updated Desktop Admin'
    })
    expect(updated.statusCode).toBe(200)
    expect(updated.json.user.label).toBe('Updated Desktop Admin')

    const listed = await requestJson(serveDesktopAuthUsersAPI, 'GET', '/api/auth/users')
    expect(listed.statusCode).toBe(200)
    expect(listed.json.users).toEqual([
      expect.objectContaining({ xpub: 'xpub-desktop-admin', label: 'Updated Desktop Admin' })
    ])
  })
```

- [x] **Step 2: Run the auth route test and verify it fails**

Run:

```bash
npx playwright test desktop/test/unit/static-http-server-identity.spec.js -g "accepts local desktop auth user"
```

Expected: FAIL because `serveDesktopAuthUsersAPI` is not exported.

- [x] **Step 3: Implement local Desktop auth users API**

In `desktop/src/static-http-server.js`, add a small JSON store backed by
`desktop-auth-users.json` under `app.getPath('userData')`:

```js
function desktopAuthUsersPath () {
  return path.join(app.getPath('userData'), 'desktop-auth-users.json')
}

async function readDesktopAuthUsers () {
  try {
    const parsed = JSON.parse(await fs.promises.readFile(desktopAuthUsersPath(), 'utf8'))
    return Array.isArray(parsed.users) ? parsed.users : []
  } catch {
    return []
  }
}

async function writeDesktopAuthUsers (users) {
  await fs.promises.mkdir(path.dirname(desktopAuthUsersPath()), { recursive: true })
  await fs.promises.writeFile(desktopAuthUsersPath(), JSON.stringify({ users }, null, 2))
}

function sanitizeDesktopAuthUser (payload) {
  return {
    xpub: readEpmString(payload, ['xpub']),
    label: readEpmString(payload, ['label', 'name', 'dn']),
    role: readEpmString(payload, ['role']) || 'admin',
    trust_level: readEpmString(payload, ['trust_level', 'trustLevel']) || 'local'
  }
}
```

Add `serveDesktopAuthUsersAPI`:

```js
async function serveDesktopAuthUsersAPI (req, res) {
  const parsed = new URL(req.url || '/', `http://${HOST}`)
  if (parsed.pathname !== '/api/auth/users' && !parsed.pathname.startsWith('/api/auth/users/')) return false

  const users = await readDesktopAuthUsers()

  if (req.method === 'GET' && parsed.pathname === '/api/auth/users') {
    sendJSON(res, 200, { users })
    return true
  }

  if (req.method === 'POST' && parsed.pathname === '/api/auth/users') {
    const user = sanitizeDesktopAuthUser(JSON.parse(await readRequestBody(req) || '{}'))
    if (!user.xpub) {
      sendJSON(res, 400, { error: 'xpub is required' })
      return true
    }
    if (users.some(existing => existing.xpub === user.xpub)) {
      sendJSON(res, 409, { error: 'user already exists' })
      return true
    }
    users.push(user)
    await writeDesktopAuthUsers(users)
    sendJSON(res, 200, { user })
    return true
  }

  if (req.method === 'PUT' && parsed.pathname.startsWith('/api/auth/users/')) {
    const xpub = decodeURIComponent(parsed.pathname.slice('/api/auth/users/'.length))
    const user = sanitizeDesktopAuthUser({ ...(JSON.parse(await readRequestBody(req) || '{}')), xpub })
    const index = users.findIndex(existing => existing.xpub === xpub)
    if (index === -1) users.push(user)
    else users[index] = user
    await writeDesktopAuthUsers(users)
    sendJSON(res, 200, { user })
    return true
  }

  sendJSON(res, 405, { error: 'method not allowed' })
  return true
}
```

Add the route before static file serving and export `serveDesktopAuthUsersAPI`.

- [x] **Step 4: Run the auth route test and verify it passes**

Run:

```bash
npx playwright test desktop/test/unit/static-http-server-identity.spec.js -g "accepts local desktop auth user"
```

Expected: PASS.

### Task 5: Add Desktop Product Link Tests And Fix Links

**Files:**
- Create: `desktop/test/unit/app-menu.spec.js`
- Create: `desktop/test/unit/tray-sdn-links.spec.js`
- Modify: `desktop/src/app-menu.js`
- Modify: `desktop/src/tray.js`

- [x] **Step 1: Add failing app menu link test**

Create `desktop/test/unit/app-menu.spec.js`:

```js
const { test, expect } = require('@playwright/test')
const proxyquire = require('proxyquire').noCallThru()

test('desktop app menu Learn More opens SDN docs', async () => {
  const opened = []
  let capturedTemplate = null
  const initMenu = proxyquire('../../src/app-menu', {
    electron: {
      app: { name: 'Space Data Network' },
      Menu: {
        buildFromTemplate: template => {
          capturedTemplate = template
          return { template }
        },
        setApplicationMenu: () => {}
      },
      shell: { openExternal: url => opened.push(url) }
    },
    './common/logger': { info: () => {} }
  })

  initMenu()
  const help = capturedTemplate.find(item => item.role === 'help')
  help.submenu[0].click()
  expect(opened).toEqual(['https://spacedatanetwork.org/docs/'])
})
```

- [x] **Step 2: Add failing tray link test**

Create `desktop/test/unit/tray-sdn-links.spec.js` with a proxyquired tray menu
builder that asserts the About submenu opens SDN release/docs URLs instead of
IPFS Desktop README URLs. Use the existing mocks from `desktop/test/unit/mocks`
and assert `shell.openExternal` receives `https://spacedatanetwork.org/` or
`https://github.com/DigitalArsenal/space-data-network`.

- [x] **Step 3: Run the new link tests and verify they fail**

Run:

```bash
npx playwright test desktop/test/unit/app-menu.spec.js desktop/test/unit/tray-sdn-links.spec.js
```

Expected: FAIL because current links point at upstream IPFS Desktop.

- [x] **Step 4: Update app menu and tray links**

In `desktop/src/app-menu.js`, change:

```js
shell.openExternal('https://github.com/ipfs-shipyard/ipfs-desktop')
```

to:

```js
shell.openExternal('https://spacedatanetwork.org/docs/')
```

In `desktop/src/tray.js`, change user-facing product links:

```js
label: `ipfs-desktop ${VERSION}`,
click: () => { shell.openExternal(`https://github.com/ipfs-shipyard/ipfs-desktop/releases/v${VERSION}`) }
```

to:

```js
label: `Space Data Network Desktop ${VERSION}`,
click: () => { shell.openExternal('https://spacedatanetwork.org/downloads/') }
```

Change the `viewOnGitHub` target to:

```js
shell.openExternal('https://github.com/DigitalArsenal/space-data-network')
```

Keep the Kubo version link pointing to upstream Kubo releases because that row
is explicitly about the bundled upstream runtime.

- [x] **Step 5: Run the link tests and verify they pass**

Run:

```bash
npx playwright test desktop/test/unit/app-menu.spec.js desktop/test/unit/tray-sdn-links.spec.js
```

Expected: PASS.

### Task 6: Run Slice 1 Verification And Commit

**Files:**
- Modified files from Tasks 1-5.

- [x] **Step 1: Run focused Desktop identity/security tests**

Run:

```bash
npx playwright test desktop/test/unit/static-http-server-identity.spec.js desktop/test/unit/static-http-server-security.spec.js
```

Expected: PASS.

- [x] **Step 2: Run release parity contract test**

Run:

```bash
node --test deployment/release/sdn-parity-contract.test.mjs
```

Expected: PASS.

- [x] **Step 3: Run whitespace check**

Run:

```bash
git diff --check
```

Expected: no output and exit code 0.

- [x] **Step 4: Commit Slice 1**

Run:

```bash
git add deployment/release/sdn-parity-contract.json deployment/release/sdn-parity-contract.test.mjs docs/superpowers/specs/2026-06-24-cli-desktop-ui-parity-design.md desktop/src/static-http-server.js desktop/test/unit/static-http-server-identity.spec.js desktop/src/app-menu.js desktop/src/tray.js desktop/test/unit/app-menu.spec.js desktop/test/unit/tray-sdn-links.spec.js
git commit -m "feat: add desktop parity routes and contract"
```

2026-06-24 verification: the current implementation already contains the
Desktop node EPM/vCard, peer EPM/vCard, auth users, product link, tray service,
local data/search, encrypted CA metadata, and security guardrail tests. Verified
with `npm --prefix desktop exec -- playwright test test/unit/static-http-server-identity.spec.js test/unit/static-http-server-data.spec.js test/unit/app-menu.spec.js test/unit/tray-sdn-links.spec.js test/unit/tray-service.spec.js` (27 passed),
`npm --prefix desktop exec -- playwright test test/unit/static-http-server-identity.spec.js test/unit/static-http-server-security.spec.js` (20 passed),
`node --test deployment/release/sdn-parity-contract.test.mjs` (5 passed), and
`git diff --check`.

---

## Slice 2: Shared Search And Provider Interaction

### Task 7: Add Shared Search Contract Tests

**Files:**
- Modify: `sdn-server/cmd/spacedatanetwork/search_cli_test.go`
- Modify: `sdn-js/src/ui/runtime/sdn-backend-desktop.test.ts`
- Create: `desktop/test/unit/static-http-server-data.spec.js`
- Modify: `desktop/src/static-http-server.js`
- Modify: `sdn-server/cmd/spacedatanetwork/search_cli.go`
- Modify: `sdn-server/internal/api/search.go`
- Modify: `sdn-server/internal/node/advertisement_discovery.go`
- Create: `sdn-server/cmd/spacedatanetwork/live_dht_search_backend.go`
- Create: `sdn-server/cmd/spacedatanetwork/live_dht_search_backend_test.go`

- [x] Add failing tests proving CLI `search providers`, Desktop search routes,
  and UI Desktop adapter return the same field names for provider ID, provider
  name, peer ID, schema, source name, record count, and last observed time.
- [x] Add failing CLI tests for `--mode local`, `--mode daemon`, `--mode dht`,
  `--api-url`, and `--format row|table|json|csv`.
  - [x] 2026-06-24: Added CLI tests for search mode normalization, `--api` /
    `--api-url` / `--session-token` help, daemon provider search posting to
    `/api/v1/search/providers`, and live-DHT data search posting to
    `/api/v1/search/data`; verified with
    `../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'Test(Search|Providers)' -count=1`.
- [x] Implement the shared Go search row helpers and explicit daemon/live-DHT
  dispatch.
  - [x] 2026-06-24: Added `api.SearchProviderRows` /
    `api.SearchDataRows`, `api.LiveSearchBackend`, daemon wiring through
    `newLiveDHTSearchBackend(n)`, and `node.DiscoverSDNAdvertisementPeers`.
    `live-dht` mode now triggers public-DHT SDN advertisement discovery before
    returning the same shared provider/data rows, and returns a clear
    unavailable error if no live backend is wired.
- [x] Implement any remaining Desktop JSON route parity gaps against the same
  result shape.
- [x] Wire `sdn-js/src/ui/runtime/sdn-backend-desktop.ts` to the same route.
- [x] Run `go test ./cmd/spacedatanetwork -run 'TestSearch'`, the Desktop data
  route tests, and the SDN JS runtime tests touched by this slice.
- [x] Commit with message `feat: share SDN search across CLI and desktop`.
  - 2026-06-24: Current implementation verified shared CLI/Desktop/UI search
    shape and modes with `../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'Test(Search|Providers)' -count=1`,
    `npm --prefix desktop exec -- playwright test test/unit/static-http-server-data.spec.js`,
    and `npm --prefix sdn-js test -- --run src/ui/runtime/sdn-backend-desktop.test.ts`.

### Task 8: Add Provider CLI Commands

**Files:**
- Create: `sdn-server/cmd/spacedatanetwork/providers_cli.go`
- Create: `sdn-server/cmd/spacedatanetwork/providers_cli_test.go`
- Modify: `sdn-server/cmd/spacedatanetwork/main.go`
- Modify: `sdn-server/cmd/spacedatanetwork/main_test.go`

- [x] Add failing command registration tests for `providers list`,
  `providers search`, `providers show`, `providers connect`, and
  `providers descriptor`.
- [x] Add output tests for table/row, JSON, and CSV.
- [x] Implement commands using the same API/live/local search backend from
  Slice 2 Task 7.
  - [x] 2026-06-24: `providers list/search/show` now pass `--mode`,
    `--api`/`--api-url`, and `--session-token` into the same local/direct or
    daemon/live-DHT shared search path as `search providers`.
- [x] Run focused provider CLI tests and commit with message
  `feat: add provider discovery CLI`.
  - 2026-06-24: Added explicit `providers list` table, `providers search`
    JSON, and `providers show` CSV coverage against the same shared search
    fixture used by `search providers`; verified with
    `../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'Test(Search|Providers)' -count=1`.

---

## Slice 3: Identity Directory Parity

### Task 9: Complete CLI Wizard Fields And Directory Commands

**Files:**
- Modify: `sdn-server/cmd/spacedatanetwork/identity_wizard_cli.go`
- Modify: `sdn-server/cmd/spacedatanetwork/identity_wizard_cli_test.go`
- Create: `sdn-server/cmd/spacedatanetwork/identity_directory_cli.go`
- Create: `sdn-server/cmd/spacedatanetwork/identity_directory_cli_test.go`
- Modify: `sdn-server/cmd/spacedatanetwork/main.go`

- [x] Add failing tests for wizard `--set given_name=`,
  `--set family_name=`, and `--set entity_type=`.
- [x] Add failing tests for `identity directory list`, `show`, `import`, and
  `download`.
- [x] Implement the missing fields and directory commands.
- [x] Run focused identity tests and commit with message
  `feat: add identity directory CLI parity`.
  - 2026-06-24: Added wizard `given_name`, `family_name`, and `entity_type=node`
    support plus `identity directory list/show/import/download`; verified with
    `../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'TestIdentity(Directory|Wizard)' -count=1`
    and `../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'Test(Identity|Search|Providers)' -count=1`.

---

## Slice 4: Encrypted CA And Maneuver Ephemeris

### Task 10: Add CLI Workflow Entry Points

**Files:**
- Create: `sdn-server/cmd/spacedatanetwork/conjunction_cli.go`
- Create: `sdn-server/cmd/spacedatanetwork/conjunction_cli_test.go`
- Modify: `sdn-server/cmd/spacedatanetwork/main.go`
- Modify: `sdn-server/cmd/spacedatanetwork/channels_cli.go`

- [x] Add failing tests for MPE source selection, encrypted channel/grant
  selection, dry-run provenance output, and JSON export.
- [x] Implement CLI entry points that compose existing data, channel, and
  module/source-selection contracts.
- [x] Run focused CA CLI tests and commit with message
  `feat: add encrypted CA maneuver ephemeris CLI`.
  - 2026-06-24: Extended `conjunction screen` with `--dry-run`,
    source/provider/PNM/query selectors, result channel ID, module metadata,
    and JSON provenance output for private maneuver ephemeris screening.
    Verified the red/green test with
    `../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'TestRunConjunctionScreenDryRunExportsManeuverEphemerisProvenance' -count=1`
    and the focused command surface with
    `../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'Test(Conjunction|RunConjunction)' -count=1`.

### Task 11: Add Desktop/UI Workflow

**Files:**
- Modify: `sdn-js/ui/src/screens/ChannelsScreen.svelte`
- Modify: `sdn-js/ui/src/screens/LocalDataScreen.svelte`
- Modify: `sdn-js/src/ui/runtime/channels.ts`
- Modify: `sdn-js/src/ui/runtime/channels.test.ts`
- Modify: `sdn-js/tests/sdn-ui.spec.ts`

- [x] Add failing runtime and UI tests for selecting private MPE data,
  confirming encrypted grant/channel state, and exporting provenance.
- [x] Implement the UI controls without adding instructional marketing text to
  the app surface.
- [x] Run focused SDN JS/UI tests and commit with message
  `feat: add encrypted CA maneuver ephemeris UI`.
  - 2026-06-24: Extended the Desktop-local conjunction route, SDN JS
    Desktop/remote backend payload contract, and Svelte Conjunction screen with
    private source selectors, result channel ID, module metadata, and
    provenance export. Verified with
    `npm --prefix desktop exec -- playwright test test/unit/static-http-server-data.spec.js`,
    `npm --prefix sdn-js test -- --run src/ui/runtime/sdn-backend-desktop.test.ts src/ui/runtime/sdn-backend-remote.test.ts src/ui/conjunction-ui-source.test.ts`,
    and `git diff --check`.

---

## Slice 5: Lifecycle, Remove, And Signed Update

### Task 12: Align Status, Remove, And Desktop Lifecycle

**Files:**
- Modify: `sdn-server/cmd/spacedatanetwork/service.go`
- Modify: `sdn-server/cmd/spacedatanetwork/service_test.go`
- Create: `sdn-server/cmd/spacedatanetwork/status_cli_test.go`
- Create: `desktop/test/unit/tray-service.spec.js`
- Modify: `desktop/src/tray.js`

- [x] Add failing tests for daemon health status, active install root
  discovery, data-preserve remove, purge remove, and Desktop tray status.
- [x] Implement health probing and shared remove semantics.
- [x] Run focused CLI and Desktop lifecycle tests and commit with message
  `feat: align service lifecycle parity`.
  - 2026-06-24: Added `spacedatanetwork status` daemon health probing through
    `/api/v1/data/health` so status reports `running`, `unhealthy`, or
    `unavailable` instead of `unknown`. Existing remove planning and Desktop
    tray lifecycle tests cover install-root discovery, preserve-by-default,
    explicit purge, and visible start/stop/restart controls. Verified with
    `../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'Test(WriteDaemonStatus|RootHelpListsServiceManagementCommands|RenderLaunchAgent|RenderSystemd|WindowsScheduledTask|ServiceActionPlans|PlanRemoveCurrentInstall)' -count=1`,
    `npm --prefix desktop exec -- playwright test test/unit/tray-service.spec.js`,
    and `git diff --check`.

### Task 13: Complete SDN-Owned Update Provider Flow

**Files:**
- Modify: `sdn-server/cmd/spacedatanetwork/update_cli.go`
- Modify: `sdn-server/cmd/spacedatanetwork/update_cli_test.go`
- Modify: `sdn-server/internal/update/update_test.go`
- Modify: `desktop/src/sdn-updater/**`
- Modify: `desktop/test/unit/sdn-updater-staged-install.spec.js`
- Modify: `deployment/release/build-cli-update-payload.mjs`
- Modify: `deployment/release/build-cli-update-payload.test.mjs`

- [x] Add failing tests for provider-server feed check, stage, in-place daemon
  stop/swap/restart, health verification, and rollback.
- [x] Implement the wrapped SDN update path around upstream IPFS/Kubo payloads.
- [x] Wire Desktop updater to the same trust/feed path.
- [x] Run focused update tests and commit with message
  `feat: add SDN daemon in-place updates`.

  Verification (2026-06-24):
  - RED: `../scripts/go-with-wasmedge.sh test ./internal/update -run 'TestRollbackLastRestoresPreviousBundleAndQuarantinesFailedUpdate' -count=1`
  - RED: `../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'TestHelperPostApplyRestartRollsBackWhenDaemonHealthFails' -count=1`
  - RED: `npm --prefix desktop exec -- playwright test test/unit/sdn-updater-staged-install.spec.js -g 'verifies daemon health after restarting the updated daemon'`
  - RED: `node --test deployment/release/build-cli-update-payload.test.mjs --test-name-pattern 'records wrapped upstream Kubo provenance'`
  - GREEN: `../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'Test(LoadBundleManifest|ProviderFeedIndexURL|FetchProviderUpdateCandidate|ReadHTTPSURL|HelperPostApplyRestart)' -count=1`
  - GREEN: `../scripts/go-with-wasmedge.sh test ./internal/update -count=1`
  - GREEN: `npm --prefix desktop exec -- playwright test test/unit/sdn-updater-staged-install.spec.js test/unit/feed-updater.spec.js test/unit/sdn-updater-release-feed.spec.js test/unit/sdn-updater-runtime-feeds.spec.js`
  - GREEN: `node --test deployment/release/build-cli-update-payload.test.mjs`
  - GREEN: `git diff --check`

---

## Slice 6: Release, Install, Live-DHT, Docs, And Desktop Packaging

### Task 14: Add Desktop Artifacts To Release Lane

**Files:**
- Modify: `.github/workflows/beta-release-artifacts.yml`
- Modify: `deployment/release/assemble-beta-release-artifacts.sh`
- Modify: `deployment/release/assemble-beta-release-artifacts.test.mjs`
- Modify: `deployment/release/beta-release-workflow.test.mjs`
- Modify: `deployment/release/beta-download-links.test.mjs`
- Modify: `docs/index.html`

- [x] Add failing release tests requiring Desktop DMG/ZIP, Windows installer,
  and Linux AppImage/deb/rpm artifacts where the current Electron Builder
  config supports them.
- [x] Implement artifact assembly and release body links.
- [x] Run release tests and commit with message
  `feat: release desktop artifacts`.

  Implementation landed earlier in SDN commit `b2c4b638` (`feat: add desktop
  artifacts to beta release lane`). Verification (2026-06-24): `node --test
  deployment/release/assemble-beta-release-artifacts.test.mjs
  deployment/release/beta-release-workflow.test.mjs
  deployment/release/beta-download-links.test.mjs` passed 28/28.

### Task 15: Add Published Installer Smoke Tests

**Files:**
- Modify: `.github/workflows/beta-release-artifacts.yml`
- Modify: `deployment/release/install-script.test.mjs`
- Create: `deployment/release/published-install-smoke.mjs`

- [x] Add failing tests asserting Linux/macOS use
  `curl -fsSL https://spacedatanetwork.org/install.sh | bash`, Windows uses
  `irm https://spacedatanetwork.org/install.ps1 | iex`, and neither path
  requires `gh`.
- [x] Add installer contract tests requiring both Unix and Windows installers
  to verify `spacedatanetwork show-identity` immediately after post-install
  identity initialization.
  - 2026-06-24: Verified with `node --test deployment/release/install-script.test.mjs`.
- [x] Add clean-user smoke script for init, version, status, and identity.
  - 2026-06-24: Added `deployment/release/published-install-smoke.mjs` and a post-release Linux/macOS/Windows matrix job that runs the published installer one-liners from isolated user homes.
- [x] Run installer tests and commit with message
  `test: smoke published SDN installers`.
  - 2026-06-24: Verified with `node --test deployment/release/*.test.mjs` and `git diff --check`, then committed as `test: smoke published SDN installers`.

### Task 16: Extend Live-DHT Cross-Platform Tests

**Files:**
- Modify: `.github/workflows/live-dht-cross-platform.yml`
- Modify: `deployment/release/live-dht-client-smoke.mjs`
- Modify: `deployment/release/live-dht-client-smoke.test.mjs`
- Modify: `deployment/release/live-dht-workflow.test.mjs`

- [x] Add failing workflow/static tests requiring Linux Docker, macOS, and
  Windows clients to wait at least five minutes for public Kademlia DHT
  registration.
- [x] Add smoke assertions for peer discovery, identity exchange, provider
  search, data search, and one retrieval/query path.
- [x] Run live-DHT workflow tests and commit with message
  `test: prove cross-platform live DHT parity`.
  - 2026-06-24: Added proof-category reporting and summary failures for peer discovery, identity exchange, positive-result provider search, positive-result data search, retrieval/query, and five-minute DHT wait; targeted tests passed with `node --test deployment/release/live-dht-client-smoke.test.mjs deployment/release/live-dht-summary.test.mjs deployment/release/live-dht-workflow.test.mjs`, full release tests passed with `node --test deployment/release/*.test.mjs`, and `git diff --check` passed.

### Task 17: Update Docs, README, Help, Website

**Files:**
- Modify: `README.md`
- Modify: `docs/docs.html`
- Modify: `docs/index.html`
- Modify: `desktop/src/app-menu.js`
- Modify: `desktop/src/tray.js`
- Modify: CLI command help in touched Go files
- Create or modify: `deployment/release/docs-parity.test.mjs`

- [ ] Add failing docs parity tests for one-line install, SDN domain links,
  CLI/Desktop/UI feature parity, encrypted CA, and maneuver ephemeris.
- [ ] Update docs and help text.
- [ ] Run docs parity tests and commit with message
  `docs: align SDN parity documentation`.

### Task 18: Desktop Package, Refresh, Restart

**Files:**
- Desktop build outputs generated by existing packaging scripts.

- [ ] Run the Desktop package command documented in `desktop/package.json`.
- [ ] Reinstall or refresh the local installed app.
- [ ] Restart the local Desktop app.
- [ ] Record the exact command output or blocker in the completion report.

---

## Stack Integration And Cleanup

### Task 19: Push SDN, Update Stack Pin, And Clean Stack State

**Files:**
- Modify: stack submodule pin `repos/main-packages/space-data-network`
- Modify only if needed: `coordination/active-work.md`

- [ ] Push SDN `main` after all component commits.
- [ ] In `/Users/tj/software/orbpro-stack`, update only the SDN submodule pin
  to the pushed SDN commit.
- [ ] Do not update unrelated SDK, OrbPro, SDS, or module pins in this SDN
  parity stack commit.
- [ ] Run `git submodule status`.
- [ ] Run `git submodule foreach 'git status --short --branch'`.
- [ ] Commit the SDN stack pin update with message
  `Update space-data-network parity pin`.
- [ ] Push stack `main`.
- [ ] Confirm no SDN parity work remains in stash, temp files, detached HEADs,
  or uncommitted stack state.

## Current Stash Audit

The current stack stash named
`On feature/unified-wasm-sensor-coverage: preserve stack dirty state before returning orbpro-stack to main`
contains unified sensor-coverage task packet/docs and one temporary PNG. The
SDN-relevant council rows were restored to `coordination/active-work.md` and
committed on stack `main` as `70f1fbae`. Do not apply the remaining stash for
this SDN parity work unless a direct stash inspection identifies SDN parity
source or docs inside it.

## Final Verification

- [ ] Run focused tests from every changed slice.
- [ ] Run `node --test deployment/release/*.test.mjs` or the narrower release
  set if the full command is blocked by environment requirements.
- [ ] Run Docker-based smoke tests where supported locally.
- [ ] Run GitHub Actions workflow static tests.
- [ ] Run `git submodule status`.
- [ ] Run `git submodule foreach 'git status --short --branch'`.
- [ ] Record Desktop package/reinstall/restart result or blocker.
- [ ] Confirm stack `main` is clean except unrelated pre-existing dirty
  submodules explicitly documented in the final report.
