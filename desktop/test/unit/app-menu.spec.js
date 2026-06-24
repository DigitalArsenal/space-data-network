const fs = require('fs')
const path = require('path')
const { test, expect } = require('@playwright/test')

test('desktop app menu Learn More opens SDN docs', () => {
  const source = fs.readFileSync(path.join(__dirname, '../../src/app-menu.js'), 'utf8')

  expect(source).toContain("shell.openExternal('https://spacedatanetwork.org/docs/')")
  expect(source).not.toContain("shell.openExternal('https://github.com/ipfs-shipyard/ipfs-desktop')")
})
