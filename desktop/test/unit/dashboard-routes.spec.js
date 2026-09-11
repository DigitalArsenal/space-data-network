const { test, expect } = require('@playwright/test')
const { daemonDashboardRoute } = require('../../src/dashboard/routes')

test.describe('daemon dashboard routes', () => {
  test('maps legacy desktop destinations onto the daemon dashboard', () => {
    expect(daemonDashboardRoute('/')).toBe('/node')
    expect(daemonDashboardRoute('/status')).toBe('/node')
    expect(daemonDashboardRoute('/files')).toBe('/node/data')
    expect(daemonDashboardRoute('/peers')).toBe('/nodes')
    expect(daemonDashboardRoute('/settings')).toBe('/node/settings')
  })

  test('passes new daemon routes through without a desktop release', () => {
    expect(daemonDashboardRoute('/store/modules')).toBe('/store/modules')
    expect(daemonDashboardRoute('store/listings')).toBe('/store/listings')
  })
})
