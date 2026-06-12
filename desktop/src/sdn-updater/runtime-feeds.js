const { KUBO_VERSION } = require('../common/consts')

const SDN_DESKTOP_RELEASES_URL = 'https://github.com/DigitalArsenal/space-data-network/releases/latest'
// Automatic SDN desktop updates stay opt-in (default false) until the SDN
// patch/update server is live; set SDN_DESKTOP_AUTO_UPDATES=1 to enable.
const SDN_DESKTOP_AUTO_UPDATES_ENABLED = process.env.SDN_DESKTOP_AUTO_UPDATES === '1'
const KUBO_RELEASES_API_URL = 'https://api.github.com/repos/ipfs/kubo/releases'
const KUBO_RELEASES_URL = 'https://github.com/ipfs/kubo/releases'

function normalizeKuboVersion (version = KUBO_VERSION) {
  return String(version).replace(/^[^\d]*/, '')
}

function sdnDesktopReleaseVersionUrl (version) {
  return `https://github.com/DigitalArsenal/space-data-network/releases/tag/desktop-v${version}`
}

function kuboRuntimeUpdateFeed (version = KUBO_VERSION) {
  return {
    runtime: 'kubo',
    currentVersion: normalizeKuboVersion(version),
    releasesApiUrl: KUBO_RELEASES_API_URL,
    releasesUrl: KUBO_RELEASES_URL,
    releaseVersionUrl: nextVersion => `${KUBO_RELEASES_URL}/tag/v${normalizeKuboVersion(nextVersion)}`
  }
}

module.exports = {
  KUBO_RELEASES_API_URL,
  KUBO_RELEASES_URL,
  SDN_DESKTOP_AUTO_UPDATES_ENABLED,
  SDN_DESKTOP_RELEASES_URL,
  kuboRuntimeUpdateFeed,
  normalizeKuboVersion,
  sdnDesktopReleaseVersionUrl
}
