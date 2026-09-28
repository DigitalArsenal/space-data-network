const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { test, expect } = require('@playwright/test')
const proxyquire = require('proxyquire').noCallThru()
const { validateNotaryToolAuthorizationArgs } = require('@electron/notarize/lib/validate-args')

const { hasSigningIdentity } = require('../../pkgs/macos/signing-identity')

const desktop = path.join(__dirname, '../..')

// Every variable the two hooks read. Each test starts with all of them unset.
const SIGNING_ENV = [
  'CSC_LINK', 'CSC_NAME', 'CSC_KEY_PASSWORD', 'CSC_IDENTITY_AUTO_DISCOVERY',
  'APPLE_API_KEY', 'APPLE_API_KEY_ID', 'APPLE_API_ISSUER',
  'APPLE_ID', 'APPLE_APP_SPECIFIC_PASSWORD', 'APPLE_TEAM_ID',
  'CI_PULL_REQUEST', 'CI_PULL_REQUESTS'
]

const DEVELOPER_ID = { CSC_LINK: '/certs/developer-id.p12' }
const API_KEY = {
  APPLE_API_KEY: '/runner/temp/asc-api-key.p8',
  APPLE_API_KEY_ID: 'KEYID00000',
  APPLE_API_ISSUER: '00000000-0000-0000-0000-000000000000'
}
const APPLE_ID = {
  APPLE_ID: 'release@example.org',
  APPLE_APP_SPECIFIC_PASSWORD: 'app-specific-placeholder',
  APPLE_TEAM_ID: 'TEAMID0000'
}

let savedEnv
test.beforeEach(() => {
  savedEnv = Object.fromEntries(SIGNING_ENV.map((name) => [name, process.env[name]]))
  clearSigningEnv()
})
test.afterEach(() => {
  for (const [name, value] of Object.entries(savedEnv)) {
    if (value === undefined) delete process.env[name]
    else process.env[name] = value
  }
})

function clearSigningEnv () {
  for (const name of SIGNING_ENV) delete process.env[name]
}

function useEnv (...sets) {
  clearSigningEnv()
  Object.assign(process.env, ...sets)
}

const hookContext = (platform, appOutDir = '/build/mac-arm64') => ({
  electronPlatformName: platform,
  appOutDir,
  packager: { appInfo: { productFilename: 'Space Data Network', id: 'org.spacedatanetwork.desktop' } }
})

// The afterSign hook with @electron/notarize replaced by a recorder, and no
// .env file read, so nothing is submitted and the environment is the test's.
function loadNotarizeHook () {
  const submissions = []
  const hook = proxyquire('../../pkgs/macos/notarize-build', {
    dotenv: { config: () => ({}) },
    '@electron/notarize': { notarize: async (options) => { submissions.push(options) } }
  }).default
  return { hook, submissions }
}

// The afterPack hook with codesign replaced by a recorder. xxd is answered
// from the file's real first four bytes, so Mach-O detection runs on real data.
function loadAdhocHook () {
  const codesign = []
  const execFileSync = (command, args) => {
    if (command === '/usr/bin/xxd') {
      const fd = fs.openSync(args[args.length - 1], 'r')
      const head = Buffer.alloc(4)
      const read = fs.readSync(fd, head, 0, 4, 0)
      fs.closeSync(fd)
      return `${head.subarray(0, read).toString('hex')}\n`
    }
    if (command === '/usr/bin/codesign') {
      codesign.push(args)
      return Buffer.alloc(0)
    }
    throw new Error(`unexpected command ${command}`)
  }
  const hook = proxyquire('../../pkgs/macos/adhoc-sign', {
    'node:child_process': { execFileSync }
  }).default
  return { hook, codesign }
}

// A packaged app as electron-builder leaves it before signing: the node bundle
// in Contents/Resources/sdn-node, with two Mach-O binaries (thin and fat), a
// data file, and a symlink to one of the binaries.
function packagedApp () {
  const outDir = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-adhoc-sign-'))
  const app = path.join(outDir, 'Space Data Network.app')
  const nodeBundle = path.join(app, 'Contents', 'Resources', 'sdn-node')
  fs.mkdirSync(path.join(nodeBundle, 'bin'), { recursive: true })
  fs.mkdirSync(path.join(app, 'Contents', 'MacOS'), { recursive: true })
  const put = (relative, bytes) => {
    const file = path.join(nodeBundle, relative)
    fs.writeFileSync(file, bytes)
    return file
  }
  const machO = [
    put('bin/spacedatanetwork', Buffer.from('cffaedfe0c000001', 'hex')),
    put('bin/ipfs', Buffer.from('cafebabe00000002', 'hex'))
  ]
  put('config.json', '{"network":{}}\n')
  fs.symlinkSync('spacedatanetwork', path.join(nodeBundle, 'bin', 'sdn'))
  return { outDir, app, machO }
}

const valueAfter = (args, flag) => {
  const at = args.indexOf(flag)
  return at === -1 ? undefined : args[at + 1]
}

test.describe('the two macOS signing hooks', () => {
  const states = [
    { name: 'nothing configured', env: {}, identity: false },
    { name: 'CI with no certificate secret', env: { CSC_LINK: '', CSC_KEY_PASSWORD: '', CSC_IDENTITY_AUTO_DISCOVERY: 'false' }, identity: false },
    { name: 'CI with the certificate secret', env: { ...DEVELOPER_ID, CSC_KEY_PASSWORD: 'p12-placeholder', CSC_IDENTITY_AUTO_DISCOVERY: 'true' }, identity: true },
    { name: 'a certificate file', env: DEVELOPER_ID, identity: true },
    { name: 'a keychain identity name', env: { CSC_NAME: 'Developer ID Application: Example (TEAMID0000)' }, identity: true },
    { name: 'a team id and no certificate', env: { APPLE_TEAM_ID: 'TEAMID0000' }, identity: false },
    { name: 'a certificate with signing switched off', env: { ...DEVELOPER_ID, CSC_IDENTITY_AUTO_DISCOVERY: 'false' }, identity: false }
  ]

  test('the ad-hoc hook and the notarization hook agree in every state', async () => {
    const { outDir } = packagedApp()
    try {
      for (const state of states) {
        useEnv(state.env)
        expect(hasSigningIdentity(), state.name).toBe(state.identity)

        const adhoc = loadAdhocHook()
        await adhoc.hook(hookContext('darwin', outDir))
        const adhocSigned = adhoc.codesign.length > 0

        // No notarization credentials in any state: a signed build must fail
        // here, an ad-hoc one must pass without submitting.
        const notarize = loadNotarizeHook()
        const refused = await notarize.hook(hookContext('darwin', outDir)).then(() => false, () => true)

        expect(adhocSigned, state.name).toBe(!state.identity)
        expect(refused, state.name).toBe(state.identity)
        expect(notarize.submissions, state.name).toEqual([])
      }
    } finally {
      fs.rmSync(outDir, { recursive: true, force: true })
    }
  })

  test('neither hook acts off macOS', async () => {
    const { outDir } = packagedApp()
    try {
      for (const env of [{}, { ...DEVELOPER_ID, ...API_KEY }]) {
        useEnv(env)
        const adhoc = loadAdhocHook()
        const notarize = loadNotarizeHook()
        for (const platform of ['linux', 'win32']) {
          await adhoc.hook(hookContext(platform, outDir))
          await notarize.hook(hookContext(platform, outDir))
        }
        expect(adhoc.codesign).toEqual([])
        expect(notarize.submissions).toEqual([])
      }
    } finally {
      fs.rmSync(outDir, { recursive: true, force: true })
    }
  })
})

test.describe('notarization (afterSign)', () => {
  test('submits a Developer ID build with the App Store Connect key, preferred over an Apple ID', async () => {
    useEnv(DEVELOPER_ID, API_KEY, APPLE_ID)
    const { hook, submissions } = loadNotarizeHook()

    await hook(hookContext('darwin', '/build/mac-arm64'))

    expect(submissions).toHaveLength(1)
    const [options] = submissions
    expect(options.tool).toBe('notarytool')
    expect(options.appPath).toBe('/build/mac-arm64/Space Data Network.app')
    expect(options.appBundleId).toBe('org.spacedatanetwork.desktop')
    // The installed @electron/notarize rejects mixed credential kinds, so this
    // also proves the Apple ID was not passed alongside the key.
    expect(validateNotaryToolAuthorizationArgs(options)).toMatchObject({
      appleApiKey: API_KEY.APPLE_API_KEY,
      appleApiKeyId: API_KEY.APPLE_API_KEY_ID,
      appleApiIssuer: API_KEY.APPLE_API_ISSUER
    })
  })

  test('falls back to an Apple ID when there is no App Store Connect key', async () => {
    useEnv(DEVELOPER_ID, APPLE_ID)
    const { hook, submissions } = loadNotarizeHook()

    await hook(hookContext('darwin'))

    expect(submissions).toHaveLength(1)
    expect(validateNotaryToolAuthorizationArgs(submissions[0])).toMatchObject({
      appleId: APPLE_ID.APPLE_ID,
      appleIdPassword: APPLE_ID.APPLE_APP_SPECIFIC_PASSWORD,
      teamId: APPLE_ID.APPLE_TEAM_ID
    })
  })

  test('refuses a Developer ID build whose credentials are incomplete', async () => {
    const incomplete = [
      { APPLE_API_KEY: API_KEY.APPLE_API_KEY, APPLE_API_KEY_ID: API_KEY.APPLE_API_KEY_ID },
      { APPLE_API_KEY_ID: API_KEY.APPLE_API_KEY_ID, APPLE_API_ISSUER: API_KEY.APPLE_API_ISSUER },
      { APPLE_ID: APPLE_ID.APPLE_ID, APPLE_APP_SPECIFIC_PASSWORD: APPLE_ID.APPLE_APP_SPECIFIC_PASSWORD },
      { APPLE_ID: APPLE_ID.APPLE_ID, APPLE_TEAM_ID: APPLE_ID.APPLE_TEAM_ID }
    ]
    for (const credentials of incomplete) {
      useEnv(DEVELOPER_ID, credentials)
      const { hook, submissions } = loadNotarizeHook()

      await expect(hook(hookContext('darwin')), Object.keys(credentials).join('+')).rejects.toThrow()
      expect(submissions).toEqual([])
    }
  })

  test('does not submit a pull-request build, which has no secrets by design', async () => {
    for (const flag of ['CI_PULL_REQUEST', 'CI_PULL_REQUESTS']) {
      useEnv(DEVELOPER_ID, { [flag]: 'true' })
      const { hook, submissions } = loadNotarizeHook()

      await expect(hook(hookContext('darwin'))).resolves.toBeUndefined()
      expect(submissions).toEqual([])
    }

    // A flag set to 'false' is not a pull request.
    useEnv(DEVELOPER_ID, { CI_PULL_REQUEST: 'false' })
    await expect(loadNotarizeHook().hook(hookContext('darwin'))).rejects.toThrow()
  })
})

test.describe('ad-hoc signing (afterPack)', () => {
  test('signs each Mach-O in the node bundle first, then the app with the hardened runtime', async () => {
    const { outDir, app, machO } = packagedApp()
    try {
      const { hook, codesign } = loadAdhocHook()
      await hook(hookContext('darwin', outDir))

      const targets = codesign.map((args) => args[args.length - 1])
      // The data file and the symlink are not signed; the app bundle is last.
      expect(targets.slice(0, -1).sort()).toEqual([...machO].sort())
      expect(targets[targets.length - 1]).toBe(app)
      for (const args of codesign) expect(valueAfter(args, '--sign')).toBe('-')

      // The node's binaries run without the hardened runtime, which would
      // strip the DYLD_LIBRARY_PATH fallback they resolve WasmEdge through.
      for (const args of codesign.slice(0, -1)) expect(args).not.toContain('--options')

      const appArgs = codesign[codesign.length - 1]
      expect(valueAfter(appArgs, '--options')).toBe('runtime')
      const plist = fs.readFileSync(valueAfter(appArgs, '--entitlements'), 'utf8')
      const granted = [...plist.matchAll(/<key>([^<]+)<\/key>\s*<true\/>/g)].map((match) => match[1])
      // What WasmEdge's AOT code needs under the hardened runtime.
      expect(granted).toEqual(expect.arrayContaining([
        'com.apple.security.cs.allow-jit',
        'com.apple.security.cs.allow-unsigned-executable-memory',
        'com.apple.security.cs.disable-library-validation'
      ]))
    } finally {
      fs.rmSync(outDir, { recursive: true, force: true })
    }
  })
})

test.describe('macOS packaging configuration', () => {
  test('builds one app per architecture, ships the node beside the asar, and runs the hooks above', () => {
    const builder = fs.readFileSync(path.join(desktop, 'electron-builder.yml'), 'utf8')
    const hook = (name) => {
      const match = builder.match(new RegExp(`^${name}:\\s*'([^']+)'`, 'm'))
      return match && path.resolve(desktop, match[1])
    }

    expect(builder).toContain('appId: org.spacedatanetwork.desktop')
    expect(builder).toContain('productName: Space Data Network')
    expect(builder).toContain('to: sdn-node')
    expect(builder).toContain("arch: ['arm64', 'x64']")
    expect(builder).not.toContain("arch: ['universal']")
    expect(builder).not.toContain('azureSignOptions')
    expect(hook('afterPack')).toBe(require.resolve('../../pkgs/macos/adhoc-sign'))
    expect(hook('afterSign')).toBe(require.resolve('../../pkgs/macos/notarize-build'))
  })
})
