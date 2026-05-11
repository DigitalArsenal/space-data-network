import { expect, test, type Page } from '@playwright/test';

const realPeerId = '16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4';

test.beforeEach(async ({ page }) => {
  await installSdnFixtures(page);
});

test('node route renders the three SDN product navigation items only', async ({ page }) => {
  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/node');

  const nav = page.getByRole('navigation', { name: 'Primary' });
  await expect(nav.getByRole('link', { name: 'Node' })).toBeVisible();
  await expect(nav.getByRole('link', { name: 'Peers' })).toBeVisible();
  await expect(nav.getByRole('link', { name: 'Data' })).toBeVisible();
  await expect(nav.getByText('Local Data')).toHaveCount(0);
  await expect(nav.getByText('Status')).toHaveCount(0);
  await expect(nav.getByText('Files')).toHaveCount(0);
});

test('peers route renders SDN peer fixtures through the backend adapter', async ({ page }) => {
  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/peers');

  await expect(page.getByRole('heading', { level: 1, name: 'Peers' })).toBeVisible();
  await expect(page.getByText('CelesTrak Provider')).toBeVisible();
  await expect(page.getByText(realPeerId)).toBeVisible();
});

test('data route renders storage and degraded SQL workbench state', async ({ page }) => {
  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/data');

  await expect(page.getByRole('heading', { name: 'Data' })).toBeVisible();
  await expect(page.getByText('Pins And Stored Objects')).toBeVisible();
  await expect(page.getByText('SQL Workbench')).toBeVisible();
  await expect(page.getByText('STARLINK-34967')).toBeVisible();
});

test('explore route renders CID inspection with the configured gateway', async ({ page }) => {
  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/explore/bafySdnFixture');

  await expect(page.getByRole('heading', { level: 1, name: 'Data' })).toBeVisible();
  await expect(page.getByText('bafySdnFixture')).toBeVisible();
  await expect(page.getByRole('link', { name: 'Open Gateway' })).toHaveAttribute(
    'href',
    'http://127.0.0.1:8081/ipfs/bafySdnFixture',
  );
});

test('command buttons use operational radii rather than fully rounded bubbles', async ({ page }) => {
  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/node');

  const radii = await page.locator('button').evaluateAll((buttons) => buttons.map((button) => {
    const radius = window.getComputedStyle(button).borderRadius.split(' ')[0] ?? '0px';
    return Number.parseFloat(radius);
  }));

  expect(radii.length).toBeGreaterThan(0);
  expect(radii.every((radius) => radius <= 12)).toBe(true);
});

test('SDN product routes do not navigate into upstream /webui', async ({ page }) => {
  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/node');

  await page.getByRole('link', { name: 'Peers' }).click();
  await expect(page).not.toHaveURL(/\/webui/);
  await page.getByRole('link', { name: 'Data' }).click();
  await expect(page).not.toHaveURL(/\/webui/);
});

test('node self EPM email stays visible after the persisted EPM reloads', async ({ page }) => {
  await page.route('**/api/identity/epms', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        epms: [
          {
            id: 'self',
            kind: 'node-self',
            epm_json: {
              dn: 'Space Data Network Desktop',
              peer_id: '12D3KooWNZMVqKBHke7bQJ6JTs2zp13DTZu441UNs6hZcZ3bUwMs',
              agent_version: 'kubo/0.39.0/sdn-desktop',
              email: 'persisted-node@example.invalid',
            },
          },
        ],
      }),
    });
  });

  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/node');

  await expect(page.getByText('persisted-node@example.invalid')).toBeVisible();
});

test('captures desktop and mobile SDN UI screenshots', async ({ page }, testInfo) => {
  for (const route of ['node', 'peers', 'data']) {
    await page.goto(`/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/${route}`);
    await assertVisualGuardrails(page);
    const screenshot = await page.screenshot({
      path: testInfo.outputPath(`${route}-${testInfo.project.name}.png`),
      fullPage: true,
    });
    expect(screenshot.length).toBeGreaterThan(10_000);
  }
});

async function assertVisualGuardrails(page: Page): Promise<void> {
  const metrics = await page.evaluate(() => {
    const root = window.getComputedStyle(document.documentElement);
    const cards = Array.from(document.querySelectorAll<HTMLElement>('.sdn-card'));
    const controls = Array.from(document.querySelectorAll<HTMLElement>('button, a, input, .sdn-button, .sdn-input'));
    return {
      scrollWidth: document.documentElement.scrollWidth,
      viewportWidth: window.innerWidth,
      bodyTextLength: document.body.innerText.trim().length,
      tokens: {
        bg: root.getPropertyValue('--sdn-bg').trim(),
        surface: root.getPropertyValue('--sdn-surface').trim(),
        blue: root.getPropertyValue('--sdn-blue').trim(),
        green: root.getPropertyValue('--sdn-green').trim(),
        amber: root.getPropertyValue('--sdn-amber').trim(),
        red: root.getPropertyValue('--sdn-red').trim(),
        purple: root.getPropertyValue('--sdn-purple').trim(),
      },
      cardRadii: cards.map((card) => Number.parseFloat(window.getComputedStyle(card).borderRadius)),
      controlRadii: controls.map((control) => Number.parseFloat(window.getComputedStyle(control).borderRadius)),
    };
  });

  expect(metrics.bodyTextLength).toBeGreaterThan(100);
  expect(metrics.scrollWidth).toBeLessThanOrEqual(metrics.viewportWidth + 1);
  expect(metrics.tokens).toEqual({
    bg: '#050506',
    surface: '#111318',
    blue: '#0a84ff',
    green: '#30d158',
    amber: '#ffd60a',
    red: '#ff453a',
    purple: '#bf5af2',
  });
  expect(metrics.cardRadii.length).toBeGreaterThan(0);
  expect(metrics.cardRadii.every((radius) => radius <= 12)).toBe(true);
  expect(metrics.controlRadii.length).toBeGreaterThan(0);
  expect(metrics.controlRadii.every((radius) => radius <= 12)).toBe(true);
}

async function installSdnFixtures(page: Page): Promise<void> {
  await page.route('**/api/node/epm/json', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        dn: 'Space Data Network Desktop',
        peer_id: '12D3KooWNZMVqKBHke7bQJ6JTs2zp13DTZu441UNs6hZcZ3bUwMs',
        agent_version: 'kubo/0.39.0/sdn-desktop',
      }),
    });
  });
  await page.route('**/api/peers/sdn', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify([
        {
          id: realPeerId,
          name: 'CelesTrak Provider',
          addrs: [`/ip4/167.172.219.213/tcp/4001/p2p/${realPeerId}`],
          trust_level: 'trusted',
          metadata: {
            agent_version: 'spacedatanetwork/1.0.3',
            protocols: '/space-data-network/module-delivery/1.0.0',
          },
        },
      ]),
    });
  });
  await page.route('**/api/v0/repo/stat', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({ RepoSize: 1_200_000_000, StorageMax: 10_000_000_000 }),
    });
  });
  await page.route('**/api/v1/data/objects', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        objects: [
          {
            id: 'omm-starlink-34967',
            label: 'STARLINK-34967',
            schema: 'OMM.fbs',
            source: 'celestrak-gp',
            state: 'stored',
          },
        ],
      }),
    });
  });
}
