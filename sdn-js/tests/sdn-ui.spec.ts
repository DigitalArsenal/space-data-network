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
  await expect(nav.getByRole('link', { name: 'Local Data' })).toBeVisible();
  await expect(nav.getByText('Status')).toHaveCount(0);
  await expect(nav.getByText('Files')).toHaveCount(0);
});

test('peers route renders SDN peer fixtures through the backend adapter', async ({ page }) => {
  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/peers');

  await expect(page.getByRole('heading', { level: 1, name: 'Peers' })).toBeVisible();
  await expect(page.getByText('CelesTrak Provider')).toBeVisible();
  await expect(page.getByText(realPeerId)).toBeVisible();
});

test('local-data route renders storage and degraded SQL workbench state', async ({ page }) => {
  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/local-data');

  await expect(page.getByRole('heading', { name: 'Local Data' })).toBeVisible();
  await expect(page.getByText('Pins And Stored Objects')).toBeVisible();
  await expect(page.getByText('SQL Workbench')).toBeVisible();
  await expect(page.getByText('STARLINK-34967')).toBeVisible();
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
  await page.getByRole('link', { name: 'Local Data' }).click();
  await expect(page).not.toHaveURL(/\/webui/);
});

test('captures desktop and mobile SDN UI screenshots', async ({ page }, testInfo) => {
  for (const route of ['node', 'peers', 'local-data']) {
    await page.goto(`/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/${route}`);
    await page.screenshot({
      path: testInfo.outputPath(`${route}-${testInfo.project.name}.png`),
      fullPage: true,
    });
  }
});

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
