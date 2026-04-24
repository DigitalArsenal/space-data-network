const fs = require('fs')
const path = require('path')
const { test, expect } = require('@playwright/test')

test.describe('SDN dashboard window', () => {
  test('loads the shared intro page before the dashboard app', () => {
    const source = fs.readFileSync(path.join(__dirname, '../../src/dashboard/index.js'), 'utf8')

    expect(source).toContain('sdn-intro.html')
    expect(source).toContain('loadIntroPage')
  })
})
