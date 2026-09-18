import { defineConfig, devices } from '@playwright/test';

/**
 * The BROWSER half of the go-libp2p interop gate.
 *
 * src/go-libp2p-interop.test.ts dials a real go-libp2p host, but it runs under
 * Node: it proves the protocol code works, not that a BROWSER can reach a node.
 * Those are different claims. A browser has no TCP and no plain websocket to a
 * node's raw port; its unrelayed paths are webtransport and webrtc-direct, both
 * of which authenticate a self-signed certificate by hash, and both of which
 * live entirely in browser APIs (WebTransport, RTCPeerConnection) that do not
 * exist in Node. Node interop passing says nothing about either.
 *
 * Headless only — the owner's machine must never be interrupted by a test
 * window.
 */
export default defineConfig({
  testDir: './e2e',
  testMatch: /browser-direct-interop\.spec\.ts/,
  forbidOnly: Boolean(process.env.CI),
  retries: 0,
  workers: 1,
  timeout: 180_000,
  reporter: [['list']],
  use: {
    headless: true,
    trace: 'retain-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
