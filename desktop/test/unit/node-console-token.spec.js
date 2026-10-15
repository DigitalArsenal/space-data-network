const { test, expect } = require('@playwright/test')
const proxyquire = require('proxyquire').noCallThru()

const {
  CONSOLE_COOKIE_NAME,
  CONSOLE_TOKEN_ENV,
  consoleCookie,
  daemonEnv,
  generateConsoleToken,
  isConsoleNavigation
} = require('../../src/node/console-token')

// Security audit 2026-10-15, SEC-012: the node admits its console only with
// the per-launch secret this shell holds. These names must match sdn-server
// internal/auth/console_gate.go (ConsoleTokenEnv, ConsoleCookieName).
test.describe('the per-launch console secret', () => {
  test('uses the names the node reads', () => {
    expect(CONSOLE_TOKEN_ENV).toBe('SDN_CONSOLE_TOKEN')
    expect(CONSOLE_COOKIE_NAME).toBe('sdn_console')
  })

  test('is 256 random bits, fresh every launch', () => {
    const a = generateConsoleToken()
    const b = generateConsoleToken()
    expect(a).toMatch(/^[A-Za-z0-9_-]{43}$/)
    expect(b).not.toBe(a)
  })

  test('reaches the daemon in its environment and nowhere else', () => {
    const token = generateConsoleToken()
    const env = daemonEnv({ PATH: '/usr/bin' }, token)
    expect(env[CONSOLE_TOKEN_ENV]).toBe(token)
    expect(env.PATH).toBe('/usr/bin')
    expect(() => daemonEnv({}, '')).toThrow(/too short/)
    expect(() => daemonEnv({}, 'short')).toThrow(/too short/)
  })

  test('rides in an HttpOnly, SameSite=Strict cookie on exactly the node origin', () => {
    const token = generateConsoleToken()
    const cookie = consoleCookie('http://127.0.0.1:52011/', token)
    expect(cookie).toEqual({
      url: 'http://127.0.0.1:52011',
      name: 'sdn_console',
      value: token,
      path: '/',
      httpOnly: true,
      secure: false,
      sameSite: 'strict'
    })
    expect(consoleCookie('https://localhost:52011/admin', token).secure).toBe(true)
  })

  test('is never sent to an origin that is not loopback', () => {
    const token = generateConsoleToken()
    expect(() => consoleCookie('http://rebind.evil.example:52011/', token)).toThrow(/non-loopback/)
    expect(() => consoleCookie('http://10.0.0.5:52011/', token)).toThrow(/non-loopback/)
  })
})

test.describe('the supervisor', () => {
  const { NodeSupervisor } = proxyquire('../../src/node/supervisor', {
    '../common/logger': { info () {}, warn () {}, error () {}, debug () {}, logsPath: '' }
  })

  test('keeps the console secret out of the state the window receives', () => {
    const supervisor = new NodeSupervisor({ userDataPath: '/u', appPath: '/a', logsPath: '/l' })
    supervisor.consoleSecret = generateConsoleToken()
    const snapshot = supervisor.snapshot()
    expect(JSON.stringify(snapshot)).not.toContain(supervisor.consoleSecret)
    expect(supervisor.consoleToken).toBe(supervisor.consoleSecret)
  })
})

// Security audit 2026-10-15, round 2: the console cookie is not port-scoped,
// so the window that carries it must never navigate (by link or redirect) off
// the dashboard's own origin.
test.describe('the console window', () => {
  function fakeElectron () {
    const opened = []
    class FakeBrowserWindow {
      constructor () {
        this.handlers = {}
        this.webContents = {
          on: (name, fn) => { this.handlers[name] = fn },
          setWindowOpenHandler: (fn) => { this.openHandler = fn },
          session: { cookies: { set: async () => {} } },
          isLoading: () => false,
          send () {},
          once () {}
        }
        FakeBrowserWindow.last = this
      }

      once () {}
      on () {}
      isDestroyed () { return false }
      loadFile () { return Promise.resolve() }
      loadURL () { return Promise.resolve() }
    }
    return {
      opened,
      electron: {
        BrowserWindow: FakeBrowserWindow,
        shell: { openExternal: async (url) => { opened.push(url) } }
      }
    }
  }

  function navigate (win, name, url) {
    let prevented = false
    win.handlers[name]({ preventDefault () { prevented = true } }, url)
    return prevented
  }

  test('stays on the dashboard origin', async () => {
    const { electron, opened } = fakeElectron()
    const { MainWindow } = proxyquire('../../src/window', {
      electron,
      './common/logger': { info () {}, warn () {}, error () {}, debug () {} }
    })
    const main = new MainWindow()
    main.setConsoleTokenProvider(() => generateConsoleToken())
    main.create()
    const win = electron.BrowserWindow.last
    expect(typeof win.handlers['will-navigate']).toBe('function')
    expect(typeof win.handlers['will-redirect']).toBe('function')

    // The status page: nothing to navigate to.
    expect(navigate(win, 'will-navigate', 'http://127.0.0.1:52011/')).toBe(true)

    main.showDashboard('http://127.0.0.1:52011/')
    await new Promise((resolve) => setImmediate(resolve))
    expect(navigate(win, 'will-navigate', 'http://127.0.0.1:52011/admin')).toBe(false)
    expect(navigate(win, 'will-navigate', 'http://127.0.0.1:8000/')).toBe(true)
    expect(navigate(win, 'will-navigate', 'http://localhost:52011/')).toBe(true)
    expect(navigate(win, 'will-navigate', 'https://evil.example/')).toBe(true)
    expect(navigate(win, 'will-navigate', 'file:///etc/passwd')).toBe(true)
    expect(navigate(win, 'will-navigate', 'http://user:pw@127.0.0.1:52011/')).toBe(true)
    expect(navigate(win, 'will-redirect', 'http://127.0.0.1:52011/admin/')).toBe(false)
    expect(navigate(win, 'will-redirect', 'http://127.0.0.1:9/')).toBe(true)
    await new Promise((resolve) => setImmediate(resolve))
    expect(opened).toEqual(['http://127.0.0.1:52011/', 'http://127.0.0.1:8000/', 'http://localhost:52011/', 'https://evil.example/', 'http://user:pw@127.0.0.1:52011/'])

    // A window.open to a non-web scheme is not handed to the OS.
    expect(win.openHandler({ url: 'file:///Applications/Calculator.app' })).toEqual({ action: 'deny' })
    await new Promise((resolve) => setImmediate(resolve))
    expect(opened).not.toContain('file:///Applications/Calculator.app')
  })

  test('isConsoleNavigation refuses everything off the dashboard origin', () => {
    expect(isConsoleNavigation(null, 'http://127.0.0.1:1/')).toBe(false)
    expect(isConsoleNavigation('http://127.0.0.1:1/', 'http://127.0.0.1:1/x')).toBe(true)
    expect(isConsoleNavigation('http://127.0.0.1:1/', 'http://127.0.0.1:2/x')).toBe(false)
    expect(isConsoleNavigation('http://127.0.0.1:1/', 'javascript:alert(1)')).toBe(false)
    expect(isConsoleNavigation('http://127.0.0.1:1/', 'not a url')).toBe(false)
  })
})
