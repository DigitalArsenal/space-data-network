// @ts-check
const fs = require('fs')
const path = require('path')
const { spawn } = require('child_process')

const desktopRoot = path.resolve(__dirname, '..')
const repositoryRoot = path.resolve(desktopRoot, '..')
const outputRoot = path.join(desktopRoot, 'build', 'sdn-runtime')
const binaryName = process.platform === 'win32' ? 'spacedatanetwork.exe' : 'spacedatanetwork'
const kuboName = process.platform === 'win32' ? 'ipfs.exe' : 'ipfs'

function requiredDirectory (value, label) {
  if (!value) throw new Error(`${label} is required`)
  const resolved = path.resolve(value)
  if (!fs.statSync(resolved).isDirectory()) throw new Error(`${label} is not a directory: ${resolved}`)
  return resolved
}

function run (command, args, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { ...options, stdio: 'inherit' })
    child.on('error', reject)
    child.on('close', code => {
      if (code === 0) return resolve()
      reject(new Error(`${command} exited with code ${code}`))
    })
  })
}

function makePortableSymlinks (libDir) {
  for (const entry of fs.readdirSync(libDir, { withFileTypes: true })) {
    if (!entry.isSymbolicLink()) continue
    const linkPath = path.join(libDir, entry.name)
    const target = fs.readlinkSync(linkPath)
    if (!path.isAbsolute(target)) continue
    fs.unlinkSync(linkPath)
    fs.symlinkSync(path.basename(target), linkPath)
  }
}

async function makeDarwinRuntimePortable (sdnBinary, wasmedgeRoot) {
  if (process.platform !== 'darwin') return
  const sourceInstallName = path.join(wasmedgeRoot, 'lib', 'libwasmedge.0.dylib')
  const packagedInstallName = '@executable_path/../runtime/wasmedge/lib/libwasmedge.0.dylib'
  await run('install_name_tool', ['-change', sourceInstallName, packagedInstallName, sdnBinary])
}

async function main () {
  const wasmedgeRoot = requiredDirectory(process.env.WASMEDGE_DIR, 'WASMEDGE_DIR')
  const sdnBinary = path.join(outputRoot, 'bin', binaryName)
  const kuboBinary = require('kubo').path()
  const walletModules = path.join(desktopRoot, 'node_modules')
  const walletWasmDist = path.join(walletModules, 'hd-wallet-wasm', 'dist')
  const walletUIDist = path.join(walletModules, 'hd-wallet-ui', 'dist')
  requiredDirectory(walletWasmDist, 'hd-wallet-wasm dist')
  requiredDirectory(walletUIDist, 'hd-wallet-ui dist')

  fs.rmSync(outputRoot, { recursive: true, force: true })
  fs.mkdirSync(path.dirname(sdnBinary), { recursive: true })
  fs.mkdirSync(path.join(outputRoot, 'runtime', 'kubo'), { recursive: true })
  fs.mkdirSync(path.join(outputRoot, 'runtime', 'modules'), { recursive: true })
  fs.mkdirSync(path.join(outputRoot, 'runtime', 'ui'), { recursive: true })

  const goBuild = [
    '-n', '10',
    path.join(repositoryRoot, 'scripts', 'go-with-wasmedge.sh'),
    'build', '-trimpath', '-o', sdnBinary, './cmd/spacedatanetwork'
  ]
  await run('nice', goBuild, { cwd: path.join(repositoryRoot, 'sdn-server'), env: process.env })

  fs.copyFileSync(kuboBinary, path.join(outputRoot, 'runtime', 'kubo', kuboName))
  const stagedWasmEdgeLib = path.join(outputRoot, 'runtime', 'wasmedge', 'lib')
  fs.cpSync(path.join(wasmedgeRoot, 'lib'), stagedWasmEdgeLib, { recursive: true })
  makePortableSymlinks(stagedWasmEdgeLib)
  await makeDarwinRuntimePortable(sdnBinary, wasmedgeRoot)

  const walletWasmDest = path.join(outputRoot, 'runtime', 'ui', 'wallet-wasm')
  const walletUIDest = path.join(outputRoot, 'runtime', 'ui', 'wallet-ui')
  await run(path.join(repositoryRoot, 'deployment', 'wallet-wasm', 'stage-wallet-wasm.sh'), [walletWasmDest, walletUIDest], {
    cwd: repositoryRoot,
    env: { ...process.env, SDN_WALLET_NODE_MODULES: walletModules }
  })
  fs.copyFileSync(
    path.join(walletWasmDest, 'hd-wallet-wasi.wasm'),
    path.join(outputRoot, 'runtime', 'modules', 'hd-wallet-wasi.wasm')
  )

  fs.chmodSync(sdnBinary, 0o755)
  fs.chmodSync(path.join(outputRoot, 'runtime', 'kubo', kuboName), 0o755)
  fs.writeFileSync(path.join(outputRoot, 'manifest.json'), `${JSON.stringify({
    schema: 'org.spacedatanetwork.desktop-runtime.v1',
    version: require('../package.json').version,
    platform: process.platform,
    arch: process.arch,
    devPayments: false
  }, null, 2)}\n`)

  console.log(`[sdn-runtime] staged ${outputRoot}`)
}

main().catch(err => {
  console.error(`[sdn-runtime] ${err.message || err}`)
  process.exitCode = 1
})
