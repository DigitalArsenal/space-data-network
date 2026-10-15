const { test, expect } = require('@playwright/test')
const proxyquire = require('proxyquire').noCallThru()

const {
  CONSOLE_COOKIE_NAME,
  CONSOLE_TOKEN_ENV,
  consoleCookie,
  daemonEnv,
  generateConsoleToken
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
