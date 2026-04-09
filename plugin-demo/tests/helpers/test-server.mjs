/**
 * SDN Test Server Helper
 *
 * Builds and launches a local SDN server on ephemeral ports for integration
 * testing. The server runs with auth disabled and TOR disabled so tests can
 * exercise the API directly.
 *
 * Usage:
 *   import { startTestServer, stopTestServer } from './helpers/test-server.mjs';
 *   const server = await startTestServer();
 *   // ... run tests against server.adminUrl ...
 *   await stopTestServer(server);
 */

import { execSync, spawn } from 'node:child_process';
import { mkdtempSync, writeFileSync, rmSync, statSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { createServer } from 'node:net';

const VERBOSE = process.env.SDN_TEST_VERBOSE === '1';

/**
 * Find an available TCP port by briefly binding to port 0.
 */
function findOpenPort() {
  return new Promise((resolve, reject) => {
    const srv = createServer();
    srv.listen(0, '127.0.0.1', () => {
      const port = srv.address().port;
      srv.close(() => resolve(port));
    });
    srv.on('error', reject);
  });
}

/**
 * Wait for a URL to return HTTP 200 (or any non-error response).
 */
async function waitForReady(url, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const resp = await fetch(url, { signal: AbortSignal.timeout(2000) });
      if (resp.ok || resp.status < 500) return true;
    } catch {
      // Server not ready yet
    }
    await new Promise(r => setTimeout(r, 300));
  }
  throw new Error(`Server not ready at ${url} after ${timeoutMs}ms`);
}

/**
 * Build the SDN server binary if needed.
 * Returns the path to the built binary.
 */
function buildServer(serverDir) {
  const binaryPath = join(serverDir, 'spacedatanetwork-test');

  // If the binary already exists (pre-built by CI), skip the rebuild.
  if (existsSync(binaryPath)) {
    if (VERBOSE) console.log(`Using pre-built SDN server at ${binaryPath}`);
    return binaryPath;
  }

  if (VERBOSE) console.log(`Building SDN server → ${binaryPath}`);

  // Use Apple's system clang on macOS to avoid linker issues with Homebrew LLVM
  // targeting SDKs that may not be installed (e.g. MacOSX26.sdk).
  const env = { ...process.env };
  if (process.platform === 'darwin') {
    if (!env.CC) {
      try {
        execSync('/usr/bin/clang --version', { stdio: 'ignore' });
        env.CC = '/usr/bin/clang';
      } catch { /* no system clang, use default */ }
    }
    // Fix CGO sysroot: find the highest installed SDK and set CGO_CFLAGS/CGO_LDFLAGS
    if (!env.CGO_CFLAGS) {
      try {
        const sdk = execSync('xcrun --sdk macosx --show-sdk-path 2>/dev/null', { encoding: 'utf8' }).trim();
        if (sdk) {
          const sysrootFlag = `-isysroot ${sdk}`;
          env.CGO_CFLAGS = (env.CGO_CFLAGS ? env.CGO_CFLAGS + ' ' : '') + sysrootFlag;
          env.CGO_LDFLAGS = (env.CGO_LDFLAGS ? env.CGO_LDFLAGS + ' ' : '') + sysrootFlag;
        }
      } catch { /* xcrun not available, proceed without sysroot fix */ }
    }
  }

  execSync(`go build -o ${binaryPath} ./cmd/spacedatanetwork`, {
    cwd: serverDir,
    stdio: VERBOSE ? 'inherit' : 'ignore',
    env,
    timeout: 120000,
  });

  return binaryPath;
}

/**
 * Start a test SDN server with ephemeral ports and disabled auth/TOR.
 *
 * @returns {Object} server handle with:
 *   - adminUrl: HTTP base URL (e.g. "http://127.0.0.1:19001")
 *   - adminPort: admin port number
 *   - libp2pPort: libp2p TCP port
 *   - wsPort: WebSocket port
 *   - dataDir: temporary data directory
 *   - process: child process handle
 *   - configPath: path to test config
 */
export async function startTestServer(opts = {}) {
  const repoRoot = opts.repoRoot || join(import.meta.dirname, '..', '..', '..');
  const serverDir = join(repoRoot, 'sdn-server');

  // Build server
  const binary = buildServer(serverDir);

  // Find open ports
  const [adminPort, libp2pPort, wsPort] = await Promise.all([
    findOpenPort(),
    findOpenPort(),
    findOpenPort(),
  ]);

  // Create temp data directory
  const dataDir = mkdtempSync(join(tmpdir(), 'sdn-test-'));

  // Write test config — auth disabled, TOR disabled, publishing enabled
  const config = {
    mode: 'full',
    network: {
      listen: [
        `/ip4/127.0.0.1/tcp/${libp2pPort}`,
        `/ip4/127.0.0.1/tcp/${wsPort}/ws`,
      ],
      bootstrap: [],
      edge_relays: [],
      max_connections: 100,
      enable_relay: false,
    },
    storage: {
      path: dataDir,
      max_size: '100MB',
      gc_interval: '1h',
    },
    schemas: {
      validate: false,  // Don't require flatc WASM for validation
      strict: false,
    },
    security: {},
    tor: {
      enabled: false,
    },
    peers: {
      strict_mode: false,
      enable_dht: false,
      enable_mdns: false,
    },
    admin: {
      enabled: true,
      listen_addr: `127.0.0.1:${adminPort}`,
      require_auth: false,  // Disable auth for testing
      tls_enabled: false,
    },
    publishing: {
      enabled: true,
      allowed_schemas: [],  // Allow all
      max_record_bytes: 10485760,
      default_quota_bytes: 104857600,
      min_trust_level: 'untrusted',
    },
  };

  const configPath = join(dataDir, 'config.yaml');

  // Convert to YAML manually (avoid dependency on yaml parser)
  const yaml = jsonToYaml(config);
  writeFileSync(configPath, yaml);

  if (VERBOSE) {
    console.log(`Test config:\n${yaml}`);
    console.log(`Admin: http://127.0.0.1:${adminPort}`);
    console.log(`libp2p: /ip4/127.0.0.1/tcp/${libp2pPort}`);
    console.log(`WS: /ip4/127.0.0.1/tcp/${wsPort}/ws`);
  }

  // Start server
  const proc = spawn(binary, ['daemon', '-c', configPath], {
    env: { ...process.env, SDN_KEY_PASSWORD: 'test-password-123' },
    stdio: VERBOSE ? ['ignore', 'inherit', 'inherit'] : ['ignore', 'ignore', 'ignore'],
  });

  const adminUrl = `http://127.0.0.1:${adminPort}`;

  try {
    // Wait for server readiness
    await waitForReady(`${adminUrl}/api/node/info`, 30000);
  } catch (err) {
    proc.kill('SIGTERM');
    throw new Error(`Failed to start test server: ${err.message}`);
  }

  return {
    adminUrl,
    adminPort,
    libp2pPort,
    wsPort,
    dataDir,
    process: proc,
    configPath,
    binary,
  };
}

/**
 * Stop a test server and clean up temp files.
 */
export async function stopTestServer(server) {
  if (server.process) {
    server.process.kill('SIGTERM');
    // Wait for process to exit
    await new Promise(resolve => {
      server.process.on('exit', resolve);
      setTimeout(() => {
        server.process.kill('SIGKILL');
        resolve();
      }, 5000);
    });
  }

  // Clean up temp directory
  try {
    rmSync(server.dataDir, { recursive: true, force: true });
  } catch {
    // Ignore cleanup errors
  }
}

/**
 * Minimal JSON-to-YAML converter (handles simple nested objects, no arrays of objects).
 */
function jsonToYaml(obj, indent = 0) {
  let yaml = '';
  const pad = '  '.repeat(indent);
  for (const [key, value] of Object.entries(obj)) {
    if (value === null || value === undefined) continue;
    if (typeof value === 'object' && !Array.isArray(value)) {
      yaml += `${pad}${key}:\n${jsonToYaml(value, indent + 1)}`;
    } else if (Array.isArray(value)) {
      if (value.length === 0) {
        yaml += `${pad}${key}: []\n`;
      } else {
        yaml += `${pad}${key}:\n`;
        for (const item of value) {
          if (typeof item === 'string') {
            yaml += `${pad}  - "${item}"\n`;
          } else {
            yaml += `${pad}  - ${item}\n`;
          }
        }
      }
    } else if (typeof value === 'string') {
      yaml += `${pad}${key}: "${value}"\n`;
    } else if (typeof value === 'boolean') {
      yaml += `${pad}${key}: ${value}\n`;
    } else {
      yaml += `${pad}${key}: ${value}\n`;
    }
  }
  return yaml;
}
