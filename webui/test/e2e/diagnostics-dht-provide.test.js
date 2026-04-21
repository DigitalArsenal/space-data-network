import { test, expect } from './setup/coverage.js'

const parseVersion = (agentVersion) => {
  const [, name = '', version = ''] = (agentVersion || '').split('/')
  return { name, version: version.split('-')[0] }
}

const compareVersions = (a, b) => {
  const left = a.split('.').map(segment => Number(segment) || 0)
  const right = b.split('.').map(segment => Number(segment) || 0)
  const length = Math.max(left.length, right.length)

  for (let index = 0; index < length; index++) {
    const leftValue = left[index] ?? 0
    const rightValue = right[index] ?? 0
    if (leftValue < rightValue) return -1
    if (leftValue > rightValue) return 1
  }

  return 0
}

const agentVersion = parseVersion(process.env.IPFS_RPC_VERSION)
const supportsDhtProvide = agentVersion.name === 'kubo' && compareVersions(agentVersion.version, '0.39.0') >= 0

// Locators for DHT Provide diagnostics page
const dhtProvide = {
  grid: (page) => page.locator('.dht-provide__grid'),
  connectivity: (page) => page.getByText('Connectivity', { exact: true }),
  queues: (page) => page.getByText('Queues', { exact: true }),
  schedule: (page) => page.getByText('Schedule', { exact: true }),
  operations: (page) => page.getByText('Operations', { exact: true }),
  network: (page) => page.getByText('Network', { exact: true }),
  workers: (page) => page.getByText('Workers', { exact: true }),
  status: (page) => page.locator('.dht-provide__grid').getByText(/Online|Offline/),
  uptime: (page) => page.getByText('Uptime', { exact: true }),
  reprovideInterval: (page) => page.getByText('Reprovide interval'),
  activeWorkers: (page) => page.locator('.dht-provide__grid').getByText('Active', { exact: true }),
  workerCount: (page) => page.locator('.dht-provide__grid').getByText(/\d+\s*\/\s*\d+/),
  lastUpdated: (page) => page.getByText(/Last updated at/),
  unsupportedTitle: (page) => page.getByText('Unsupported Kubo Version', { exact: true }),
  unsupportedDescription: (page) => page.getByText(new RegExp(`Your current version of Kubo \\(${process.env.IPFS_RPC_VERSION?.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') || ''}\\)`, 'i'))
}

test.describe('DHT Provide diagnostics page', () => {
  test.skip(!supportsDhtProvide, `requires kubo >= 0.39.0, got ${process.env.IPFS_RPC_VERSION || 'unknown'}`)

  test.beforeEach(async ({ page }) => {
    await page.goto('/#/diagnostics/provider')
    // wait for the stats grid to appear (indicates data has loaded)
    await expect(dhtProvide.grid(page)).toBeVisible({ timeout: 15000 })
  })

  test('should display all stat cards', async ({ page }) => {
    await expect(dhtProvide.connectivity(page)).toBeVisible()
    await expect(dhtProvide.queues(page)).toBeVisible()
    await expect(dhtProvide.schedule(page)).toBeVisible()
    await expect(dhtProvide.operations(page)).toBeVisible()
    await expect(dhtProvide.network(page)).toBeVisible()
    await expect(dhtProvide.workers(page)).toBeVisible()
  })

  test('should display connectivity status', async ({ page }) => {
    // status should be either Online or Offline
    await expect(dhtProvide.status(page)).toBeVisible()
  })

  test('should display uptime', async ({ page }) => {
    await expect(dhtProvide.uptime(page)).toBeVisible()
  })

  test('should display reprovide interval', async ({ page }) => {
    // default reprovide interval is 22h
    await expect(dhtProvide.reprovideInterval(page)).toBeVisible()
    await expect(page.getByText(/22h/)).toBeVisible()
  })

  test('should display worker counts', async ({ page }) => {
    // workers card shows "X / Y" format for active workers
    await expect(dhtProvide.activeWorkers(page)).toBeVisible()
    await expect(dhtProvide.workerCount(page)).toBeVisible()
  })

  test('should show last updated timestamp', async ({ page }) => {
    await expect(dhtProvide.lastUpdated(page)).toBeVisible()
  })
})

test.describe('DHT Provide diagnostics page unsupported kubo', () => {
  test.skip(supportsDhtProvide, `expected unsupported kubo, got ${process.env.IPFS_RPC_VERSION || 'unknown'}`)

  test('should show unsupported kubo notice', async ({ page }) => {
    await page.goto('/#/diagnostics/provider')
    await expect(dhtProvide.unsupportedTitle(page)).toBeVisible()
    await expect(dhtProvide.unsupportedDescription(page)).toBeVisible()
  })
})
