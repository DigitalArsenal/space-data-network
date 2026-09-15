#!/usr/bin/env node
// Copy a self-contained Space Data Network node bundle into the place
// electron-builder ships it from.
//
//   node desktop/scripts/stage-node-bundle.mjs --from <bundle-dir|archive>
//   SDN_DESKTOP_BUNDLE_DIR=<bundle-dir> node desktop/scripts/stage-node-bundle.mjs
//
// A bundle directory is the one build-self-contained-cli.mjs stages (it holds
// manifest.json); an archive is the .tar.gz or .zip of that directory, which is
// what the release workflow's `cli` job uploads.
//
// The staged tree is gitignored and must never be committed: it is ~400 MB of
// built binaries whose home is the release pipeline.
import { execFileSync } from 'node:child_process'
import { chmodSync, cpSync, existsSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const desktopRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const target = join(desktopRoot, 'assets', 'sdn-node')

function parseArgs (argv) {
  const options = {}
  for (let i = 0; i < argv.length; i += 1) {
    const key = argv[i]
    if (key === '--from') { options.from = argv[++i]; continue }
    if (key === '--target') { options.target = argv[++i]; continue }
    if (key === '-h' || key === '--help') { options.help = true; continue }
    throw new Error(`unknown argument: ${key}`)
  }
  return options
}

function usage () {
  console.log(`Usage: stage-node-bundle.mjs --from <bundle-dir|archive>

  --from <path>    a staged bundle directory, or its .tar.gz / .zip archive.
                   Defaults to $SDN_DESKTOP_BUNDLE_DIR.
  --target <path>  where to stage it (default ${target}).
`)
}

// A directory that holds exactly one entry, itself a bundle, is an unpacked
// archive: the archives carry a spacedatanetwork-<version>-<os>-<arch>/ root.
function findBundleRoot (path) {
  if (existsSync(join(path, 'manifest.json'))) return path
  const entries = readdirSync(path, { withFileTypes: true }).filter((entry) => entry.isDirectory())
  for (const entry of entries) {
    const candidate = join(path, entry.name)
    if (existsSync(join(candidate, 'manifest.json'))) return candidate
  }
  throw new Error(`no manifest.json under ${path}: that is not a node bundle`)
}

function extract (archive, into) {
  if (archive.endsWith('.zip')) {
    execFileSync('unzip', ['-q', archive, '-d', into], { stdio: 'inherit' })
  } else {
    execFileSync('tar', ['-xzf', archive, '-C', into], { stdio: 'inherit' })
  }
  return findBundleRoot(into)
}

// electron-builder copies extraResources with their modes, but an archive
// unpacked by a different tool, or a bundle copied off a filesystem without
// permission bits, can arrive without them. A node that cannot be executed is
// the one failure the app cannot recover from, so the bits are set here.
function makeExecutable (root) {
  const executables = [
    join(root, 'bin', 'spacedatanetwork'),
    join(root, 'bin', 'spacedatanetwork.exe'),
    join(root, 'bin', 'sdn'),
    join(root, 'runtime', 'sdn', 'spacedatanetwork'),
    join(root, 'runtime', 'kubo', 'ipfs'),
    join(root, 'runtime', 'kubo', 'ipfs.exe')
  ]
  const wasmedgeBin = join(root, 'runtime', 'wasmedge', 'bin')
  if (existsSync(wasmedgeBin)) {
    for (const entry of readdirSync(wasmedgeBin)) executables.push(join(wasmedgeBin, entry))
  }
  for (const file of executables) {
    if (existsSync(file) && statSync(file).isFile()) chmodSync(file, 0o755)
  }
}

// The staged node must carry WasmEdge INSIDE it. Every CLI bundle is linked
// against the static prefix (scripts/build-static-wasmedge.sh + link.flags), so
// a dynamic libwasmedge reference here means something upstream regressed to a
// dynamic SDK — and the app would ship depending on a library only the build
// machine has.
//
// This replaces a relocation step that REWROTE such a reference to
// @executable_path/../wasmedge/lib/<leaf>. That was right when the bundle
// staged a runtime directory; it does not any more, so the rewrite would now
// point at a path that does not exist and turn a clear build failure into a
// dyld error on a user's machine. Verify instead of repair.
function assertSelfContained (root) {
  const binary = [
    join(root, 'runtime', 'sdn', 'spacedatanetwork'),
    join(root, 'bin', 'spacedatanetwork')
  ].find((candidate) => existsSync(candidate))
  if (!binary) return

  let tool, args
  if (process.platform === 'darwin') {
    tool = 'otool'; args = ['-L', binary]
  } else if (process.platform === 'linux') {
    tool = 'ldd'; args = [binary]
  } else {
    // No dependable cross-platform equivalent on Windows; the release job
    // already fails the image build there if the binary links libwasmedge.
    return
  }

  let output
  try {
    output = execFileSync(tool, args, { encoding: 'utf8' })
  } catch {
    // ldd exits non-zero for a static binary with no dynamic section, which is
    // the answer we wanted.
    console.log(`[stage-node] ${tool} reported no dynamic dependencies`)
    return
  }
  const dynamic = output
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => /libwasmedge/i.test(line))
  if (dynamic.length > 0) {
    throw new Error(
      `staged node depends on WasmEdge outside the binary:\n  ${dynamic.join('\n  ')}\n` +
      'every bundle must link the static prefix — check WASMEDGE_DIR and scripts/go-with-wasmedge.sh'
    )
  }
  console.log('[stage-node] node is self-contained (no libwasmedge dependency)')
}

function main () {
  const options = parseArgs(process.argv.slice(2))
  if (options.help) { usage(); return }
  const from = options.from || process.env.SDN_DESKTOP_BUNDLE_DIR
  if (!from) { usage(); throw new Error('--from or SDN_DESKTOP_BUNDLE_DIR is required') }
  const source = resolve(from)
  if (!existsSync(source)) throw new Error(`no such bundle: ${source}`)
  const destination = options.target ? resolve(options.target) : target

  let workspace = ''
  let root
  try {
    if (statSync(source).isDirectory()) {
      root = findBundleRoot(source)
    } else {
      workspace = mkdtempSync(join(tmpdir(), 'sdn-node-bundle-'))
      root = extract(source, workspace)
    }
    rmSync(destination, { recursive: true, force: true })
    cpSync(root, destination, { recursive: true, verbatimSymlinks: true })
  } finally {
    if (workspace) rmSync(workspace, { recursive: true, force: true })
  }

  makeExecutable(destination)
  assertSelfContained(destination)

  const manifest = JSON.parse(readFileSync(join(destination, 'manifest.json'), 'utf8'))
  console.log(`[stage-node] staged ${manifest.os}/${manifest.arch} node ${manifest.version} into ${destination}`)
}

main()
