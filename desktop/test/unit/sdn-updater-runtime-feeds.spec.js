const fs = require('fs')
const path = require('path')
const { test, expect } = require('@playwright/test')
const {
  SDN_DESKTOP_RELEASES_URL,
  kuboRuntimeUpdateFeed,
  normalizeKuboVersion,
  sdnDesktopReleaseVersionUrl
} = require('../../src/sdn-updater/runtime-feeds')

test.describe('SDN updater release feed boundaries', () => {
  test('keeps desktop application updates on SDN release metadata', () => {
    expect(SDN_DESKTOP_RELEASES_URL).toBe('https://github.com/DigitalArsenal/space-data-network/releases/latest')
    expect(sdnDesktopReleaseVersionUrl('0.48.0')).toBe('https://github.com/DigitalArsenal/space-data-network/releases/tag/desktop-v0.48.0')
  })

  test('points Kubo runtime update checks at Kubo releases without IPFS Desktop feeds', () => {
    const feed = kuboRuntimeUpdateFeed('^0.39.0')

    expect(feed).toEqual({
      runtime: 'kubo',
      currentVersion: '0.39.0',
      releasesApiUrl: 'https://api.github.com/repos/ipfs/kubo/releases',
      releasesUrl: 'https://github.com/ipfs/kubo/releases',
      releaseVersionUrl: expect.any(Function)
    })
    expect(feed.releaseVersionUrl('v0.40.0')).toBe('https://github.com/ipfs/kubo/releases/tag/v0.40.0')
    expect(JSON.stringify(feed)).not.toContain('ipfs-desktop')
    expect(JSON.stringify(feed)).not.toContain('ipfs-shipyard')
  })

  test('keeps app updater source free of upstream IPFS Desktop release feeds', () => {
    const autoUpdaterSource = fs.readFileSync(path.join(__dirname, '../../src/auto-updater/index.js'), 'utf8')

    expect(autoUpdaterSource).toContain('SDN_DESKTOP_RELEASES_URL')
    expect(autoUpdaterSource).toContain('sdnDesktopReleaseVersionUrl')
    expect(autoUpdaterSource).not.toContain('github.com/ipfs/ipfs-desktop')
    expect(autoUpdaterSource).not.toContain('github.com/ipfs-shipyard/ipfs-desktop')
  })

  test('normalizes Kubo versions from package ranges and release tags', () => {
    expect(normalizeKuboVersion('^0.39.0')).toBe('0.39.0')
    expect(normalizeKuboVersion('v0.40.0')).toBe('0.40.0')
  })
})
