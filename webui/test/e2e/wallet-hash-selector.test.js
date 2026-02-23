import { test, expect } from './setup/coverage.js'

const badRouteHashes = [
  '/#/peers',
  '/#/peers-tab',
  '/#/peers?tab=wallet',
  '/#/peers#section',
]

test.describe('Hash-based route safety', () => {
  test('does not throw selector syntax errors for wallet/tab-like hashes', async ({ page }) => {
    for (const route of badRouteHashes) {
      const consoleErrors = []

      const onConsole = (msg) => {
        const text = msg.text()
        if (msg.type() === 'error' && /querySelector|SyntaxError/.test(text)) {
          consoleErrors.push(text)
        }
      }

      const onPageError = (err) => {
        const text = String(err?.message || err)
        if (/querySelector|SyntaxError/.test(text)) {
          consoleErrors.push(text)
        }
      }

      page.on('console', onConsole)
      page.on('pageerror', onPageError)

      await page.goto(route)
      await page.waitForLoadState('domcontentloaded')
      await expect(page.locator('body')).toBeVisible()

      page.off('console', onConsole)
      page.off('pageerror', onPageError)

      expect(consoleErrors).toEqual([])
    }
  })
})
