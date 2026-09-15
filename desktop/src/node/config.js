// @ts-check
//
// The per-user node configuration this shell writes on first run.
//
// It is written ONCE. Afterwards the file belongs to the operator: `init` and
// the daemon rewrite it as full YAML, and a hand edit must survive a restart,
// so nothing here ever rewrites an existing file. The shell reads the admin URL
// back from the node itself (`spacedatanetwork open`) rather than keeping its
// own copy of the port.
//
// The keys mirror sdn-server/internal/config. JSON is written rather than YAML
// because YAML is a superset of JSON, the node parses it with the same loader,
// and it keeps a YAML serialiser out of the shell.
const { existsSync, mkdirSync, writeFileSync } = require('node:fs')
const { createServer } = require('node:net')
const { dirname, join } = require('node:path')

/**
 * The node derives its key directory as `dirname(storage.path)/keys` and its
 * managed-Kubo repository as `<setup.data_path>/kubo`, so one home directory
 * gives the whole node a place to live under the user's data directory.
 *
 * @param {string} userDataPath
 */
function nodeHome (userDataPath) {
  return join(userDataPath, 'node')
}

/** @param {string} userDataPath */
function configPathFor (userDataPath) {
  return join(nodeHome(userDataPath), 'config.yaml')
}

/**
 * Build the first-run configuration. Pure: every port is an argument.
 *
 * @param {object} opts
 * @param {string} opts.home            the node's home directory
 * @param {number} opts.adminPort       loopback admin/API/dashboard port
 * @param {number} opts.swarmPort       libp2p TCP port
 * @param {number} opts.wsPort          libp2p websocket port
 * @param {number} opts.quicPort        libp2p QUIC (UDP) port
 * @param {number} opts.webrtcPort      libp2p WebRTC-direct (UDP) port
 */
function buildNodeConfig ({ home, adminPort, swarmPort, wsPort, quicPort, webrtcPort }) {
  return {
    mode: 'full',
    storage: {
      // keys land in <home>/keys, beside the store, because the node derives
      // the key directory from the storage path.
      path: join(home, 'data')
    },
    setup: {
      // the managed Kubo repository, TLS cache and other per-node state hang
      // off this, keeping everything inside the user's data directory.
      data_path: home
    },
    network: {
      // Free ports, chosen once. The node's defaults put the websocket
      // listener on 8080, which is exactly where the Kubo it manages binds its
      // gateway, so a default desktop node would fight itself for that port.
      listen: [
        `/ip4/0.0.0.0/tcp/${swarmPort}`,
        `/ip4/0.0.0.0/tcp/${wsPort}/ws`,
        `/ip4/0.0.0.0/udp/${quicPort}/quic-v1`,
        `/ip4/0.0.0.0/udp/${webrtcPort}/webrtc-direct`
      ]
    },
    admin: {
      enabled: true,
      // Loopback only: the dashboard is this machine's operator surface.
      listen_addr: `127.0.0.1:${adminPort}`,
      // require_auth, dev_auto_admin and ipfs_api_url are deliberately absent:
      // the node's own defaults apply, and leaving ipfs_api_url at its default
      // is what makes the node manage the bundle's Kubo itself.
      require_auth: true
    }
  }
}

/**
 * Write the first-run configuration if there is not one already.
 *
 * @param {object} opts
 * @param {string} opts.userDataPath
 * @param {() => Promise<number>} [opts.freePort] injectable for tests
 * @returns {Promise<{ path: string, home: string, created: boolean }>}
 */
async function ensureNodeConfig ({ userDataPath, freePort = findFreePort }) {
  const home = nodeHome(userDataPath)
  const path = configPathFor(userDataPath)
  if (existsSync(path)) {
    return { path, home, created: false }
  }
  const config = buildNodeConfig({
    home,
    adminPort: await freePort(),
    swarmPort: await freePort(),
    wsPort: await freePort(),
    quicPort: await freePort('udp'),
    webrtcPort: await freePort('udp')
  })
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 })
  writeFileSync(path, `${JSON.stringify(config, null, 2)}\n`, { mode: 0o600 })
  return { path, home, created: true }
}

/**
 * Ask the operating system for a port nothing is listening on.
 *
 * @param {'tcp'|'udp'} [protocol]
 * @returns {Promise<number>}
 */
function findFreePort (protocol = 'tcp') {
  if (protocol === 'udp') {
    // A UDP listener on the same number is what QUIC needs; binding a TCP
    // socket to ask the kernel for a free number is close enough for a first
    // run and avoids a second code path.
    return findFreePort('tcp')
  }
  return new Promise((resolve, reject) => {
    const server = createServer()
    server.unref()
    server.on('error', reject)
    server.listen({ host: '127.0.0.1', port: 0 }, () => {
      const address = server.address()
      const port = typeof address === 'object' && address != null ? address.port : 0
      server.close(() => (port ? resolve(port) : reject(new Error('could not find a free port'))))
    })
  })
}

module.exports = { buildNodeConfig, configPathFor, ensureNodeConfig, findFreePort, nodeHome }
