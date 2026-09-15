// @ts-check
//
// Where the bundled node lives, and the environment its child process needs.
//
// The node ships as the self-contained bundle that
// deployment/release/build-self-contained-cli.mjs stages:
//
//   <bundle>/bin/spacedatanetwork          launcher (unix) / real exe (windows)
//   <bundle>/runtime/sdn/spacedatanetwork  the real binary (unix)
//   <bundle>/runtime/wasmedge/{lib,bin}    the WasmEdge runtime it links against
//   <bundle>/runtime/kubo/ipfs             the Kubo the node manages itself
//   <bundle>/runtime/modules/*.wasm        updater + HD wallet modules
//   <bundle>/runtime/ui/*                  dashboard, webui, wallet sign-in assets
//   <bundle>/trust/update-roots.json       fleet update trust roots
//   <bundle>/manifest.json                 what makes the directory a bundle
//
// The binary resolves every one of those paths from its own location
// (sdn-server/internal/bundle), so the shell only has to spawn the right file
// with the right library path.
const { existsSync, readFileSync } = require('node:fs')
const { join, delimiter } = require('node:path')

const IS_WIN = process.platform === 'win32'
const EXE = IS_WIN ? 'spacedatanetwork.exe' : 'spacedatanetwork'

/**
 * Candidate bundle roots, most specific first.
 *
 * @param {{ resourcesPath?: string, appPath: string, env?: NodeJS.ProcessEnv }} opts
 * @returns {string[]}
 */
function bundleCandidates ({ resourcesPath, appPath, env = process.env }) {
  const candidates = []
  // CI and local packaging stage the bundle somewhere else and say so.
  if (env.SDN_DESKTOP_BUNDLE_DIR) candidates.push(env.SDN_DESKTOP_BUNDLE_DIR)
  // Packaged: electron-builder ships the bundle as extraResources.
  if (resourcesPath) candidates.push(join(resourcesPath, 'sdn-node'))
  // Development: `npm --prefix desktop run stage-node -- --from <dir>` puts it here.
  candidates.push(join(appPath, 'assets', 'sdn-node'))
  return candidates
}

/**
 * @param {string} root
 * @returns {boolean} whether root looks like a staged node bundle
 */
function isBundle (root) {
  return existsSync(join(root, 'manifest.json')) && existsSync(nodeBinary(root))
}

/**
 * The executable to spawn. On unix this is the real binary rather than the
 * bundle's `bin/` launcher script: the shell sets the same environment the
 * launcher would, and spawning the binary directly keeps the child free of a
 * /bin/sh dependency and of whatever PATH a GUI session happens to hand us.
 *
 * @param {string} root
 */
function nodeBinary (root) {
  return IS_WIN ? join(root, 'bin', EXE) : join(root, 'runtime', 'sdn', EXE)
}

/**
 * @param {{ resourcesPath?: string, appPath: string, env?: NodeJS.ProcessEnv }} opts
 * @returns {{ root: string, binary: string, version: string }}
 * @throws when no bundle is staged
 */
function resolveBundle (opts) {
  const candidates = bundleCandidates(opts)
  for (const root of candidates) {
    if (isBundle(root)) {
      return { root, binary: nodeBinary(root), version: bundleVersion(root) }
    }
  }
  throw new Error(
    `no Space Data Network node bundle found. Looked in:\n  ${candidates.join('\n  ')}\n` +
    'Stage one with: npm --prefix desktop run stage-node -- --from <bundle-dir>'
  )
}

/** @param {string} root */
function bundleVersion (root) {
  try {
    const manifest = JSON.parse(readFileSync(join(root, 'manifest.json'), 'utf8'))
    return typeof manifest.version === 'string' ? manifest.version : ''
  } catch {
    return ''
  }
}

/**
 * The child environment. WasmEdge is a dynamic library inside the bundle, so
 * the loader has to be told where it is; the module path spares the binary a
 * search. Nothing here reaches outside the bundle.
 *
 * @param {string} root
 * @param {NodeJS.ProcessEnv} [base]
 * @returns {NodeJS.ProcessEnv}
 */
function nodeEnv (root, base = process.env) {
  const wasmedge = join(root, 'runtime', 'wasmedge')
  const libDir = join(wasmedge, 'lib')
  const env = { ...base }
  env.WASMEDGE_DIR = wasmedge
  env.HD_WALLET_WASM_PATH = join(root, 'runtime', 'modules', 'hd-wallet-wasi.wasm')
  env.LD_LIBRARY_PATH = prepend(libDir, base.LD_LIBRARY_PATH)
  if (process.platform === 'darwin') {
    env.DYLD_LIBRARY_PATH = prepend(libDir, base.DYLD_LIBRARY_PATH)
  }
  if (IS_WIN) {
    env.PATH = prepend(join(wasmedge, 'bin'), prepend(join(root, 'bin'), base.PATH))
  } else {
    // A GUI process can inherit a PATH without /usr/bin; the node shells out
    // for nothing critical, but its children (Kubo) expect a sane one.
    env.PATH = prepend(join(root, 'bin'), base.PATH || '/usr/bin:/bin:/usr/sbin:/sbin')
  }
  return env
}

/**
 * @param {string} entry
 * @param {string|undefined} list
 */
function prepend (entry, list) {
  return list ? `${entry}${delimiter}${list}` : entry
}

module.exports = { bundleCandidates, isBundle, nodeBinary, nodeEnv, resolveBundle, bundleVersion, EXE }
