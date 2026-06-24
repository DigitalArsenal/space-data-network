const fs = require('fs')
const path = require('path')
const { test, expect } = require('@playwright/test')

test('desktop tray About links point at SDN product pages', () => {
  const source = fs.readFileSync(path.join(__dirname, '../../src/tray.js'), 'utf8')

  expect(source).toContain('Space Data Network Desktop ${VERSION}')
  expect(source).toContain("shell.openExternal('https://spacedatanetwork.org/downloads/')")
  expect(source).toContain("shell.openExternal('https://github.com/DigitalArsenal/space-data-network')")
  expect(source).toContain('github.com/ipfs/kubo/releases')
  expect(source).not.toContain('github.com/ipfs-shipyard/ipfs-desktop/releases')
  expect(source).not.toContain('github.com/ipfs-shipyard/ipfs-desktop/blob/master/README.md')
})
