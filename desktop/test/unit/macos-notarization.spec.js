const fs = require('node:fs')
const path = require('node:path')
const { test, expect } = require('@playwright/test')

const read = (file) => fs.readFileSync(path.join(__dirname, '../..', file), 'utf8')

test.describe('macOS signing configuration', () => {
  test('notarizes only when the Apple credentials are in the environment', () => {
    const notarize = read('pkgs/macos/notarize-build.js')

    expect(notarize).toContain('appBundleId: appId')
    expect(notarize).toContain('process.env.APPLE_ID')
    expect(notarize).toContain('process.env.APPLE_APP_SPECIFIC_PASSWORD')
    expect(notarize).toContain('teamId: process.env.APPLE_TEAM_ID')
    expect(notarize).not.toContain('io.ipfs.desktop')
  })

  test('ad-hoc signs the app and the bundled node when there is no identity', () => {
    const adhoc = read('pkgs/macos/adhoc-sign.js')

    expect(adhoc).toContain("CSC_IDENTITY_AUTO_DISCOVERY === 'false'")
    expect(adhoc).toContain("'--sign', '-'")
    expect(adhoc).toContain('sdn-node')
  })

  test('builds one app per architecture and ships the node beside the asar', () => {
    const builder = read('electron-builder.yml')

    expect(builder).toContain('appId: org.spacedatanetwork.desktop')
    expect(builder).toContain('productName: Space Data Network')
    expect(builder).toContain("afterPack: './pkgs/macos/adhoc-sign.js'")
    expect(builder).toContain('to: sdn-node')
    expect(builder).toContain("arch: ['arm64', 'x64']")
    expect(builder).not.toContain("arch: ['universal']")
    expect(builder).not.toContain('azureSignOptions')
  })
})
