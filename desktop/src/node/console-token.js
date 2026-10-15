// @ts-check
//
// The per-launch console secret (security audit 2026-10-15, SEC-012).
//
// The node admits its own console as the node's root account. Being on
// loopback is not proof of being the console: every web page in every browser
// on this machine can send requests to 127.0.0.1, and every other local
// process shares the interface. So each time the shell starts the node it:
//
//   1. generates 32 random bytes (consoleToken),
//   2. hands them to the daemon in SDN_CONSOLE_TOKEN (daemonEnv); the daemon
//      reads the variable once and removes it from its own environment,
//   3. sets the same value as an HttpOnly, SameSite=Strict, host-only cookie on
//      the node's own origin in the window's session (consoleCookie) before it
//      loads the dashboard.
//
// The value never goes to the renderer: it is not in the node state the
// window receives, and the cookie is HttpOnly.
const { randomBytes } = require('node:crypto')

/** The environment variable the daemon reads (sdn-server auth.ConsoleTokenEnv). */
const CONSOLE_TOKEN_ENV = 'SDN_CONSOLE_TOKEN'
/** The cookie the daemon reads (sdn-server auth.ConsoleCookieName). */
const CONSOLE_COOKIE_NAME = 'sdn_console'

/** @returns {string} 43 base64url characters (256 bits). */
function generateConsoleToken () {
  return randomBytes(32).toString('base64url')
}

/**
 * The daemon's environment: the base environment plus the console secret.
 *
 * @param {NodeJS.ProcessEnv} base
 * @param {string} token
 * @returns {NodeJS.ProcessEnv}
 */
function daemonEnv (base, token) {
  if (typeof token !== 'string' || token.length < 32) {
    throw new Error('console token missing or too short')
  }
  return { ...base, [CONSOLE_TOKEN_ENV]: token }
}

/**
 * The cookie details for Electron's session.cookies.set: the console secret,
 * scoped to exactly the node's origin.
 *
 * @param {string} dashboardUrl
 * @param {string} token
 */
function consoleCookie (dashboardUrl, token) {
  const url = new URL(dashboardUrl)
  if (url.hostname !== '127.0.0.1' && url.hostname !== 'localhost' && url.hostname !== '[::1]') {
    throw new Error(`refusing to send the console secret to a non-loopback origin (${url.origin})`)
  }
  return {
    url: url.origin,
    name: CONSOLE_COOKIE_NAME,
    value: token,
    path: '/',
    httpOnly: true,
    secure: url.protocol === 'https:',
    sameSite: /** @type {'strict'} */ ('strict')
  }
}

/**
 * Whether the window that holds the console cookie may navigate to targetUrl:
 * only within the dashboard's own origin. The console cookie is not
 * port-scoped, so a link or a redirect that took the window to another
 * loopback port (or anywhere else) would carry the console secret there.
 * Anything else opens in the user's browser instead.
 *
 * @param {string|null|undefined} dashboardUrl the loaded dashboard, if any
 * @param {string} targetUrl
 * @returns {boolean}
 */
function isConsoleNavigation (dashboardUrl, targetUrl) {
  if (!dashboardUrl) return false
  try {
    const from = new URL(dashboardUrl)
    const to = new URL(targetUrl)
    if (to.protocol !== 'http:' && to.protocol !== 'https:') return false
    if (to.username || to.password) return false
    return to.origin === from.origin
  } catch {
    return false
  }
}

/**
 * Whether a URL the window refuses may be handed to the user's browser.
 *
 * @param {string} targetUrl
 * @returns {boolean}
 */
function isExternalBrowserUrl (targetUrl) {
  try {
    const u = new URL(targetUrl)
    return u.protocol === 'https:' || u.protocol === 'http:'
  } catch {
    return false
  }
}

module.exports = { CONSOLE_COOKIE_NAME, CONSOLE_TOKEN_ENV, consoleCookie, daemonEnv, generateConsoleToken, isConsoleNavigation, isExternalBrowserUrl }
