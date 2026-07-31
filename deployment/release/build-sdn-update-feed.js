#!/usr/bin/env node

const fs = require('fs/promises')
const path = require('path')
const {
  SDN_UPDATE_FEED_BASE_PATH,
  SDN_UPDATE_FEED_BASE_URL,
  buildReleaseIndex,
  updateFeedRoot
} = require('../../desktop/src/sdn-updater/release-feed')

async function readJson (filePath) {
  return JSON.parse(await fs.readFile(filePath, 'utf8'))
}

async function writeJson (filePath, value) {
  await fs.mkdir(path.dirname(filePath), { recursive: true })
  await fs.writeFile(filePath, `${JSON.stringify(value, null, 2)}\n`)
}

// relativeFeedPath is the on-disk directory for a manifest, RELATIVE TO THE
// FEED ROOT.
//
// It must be relative, not absolute: the generated tree is rsync'd to whatever
// directory the serving route is pointed at (SDN_UPDATE_FEED_DIR), and that
// directory already IS the feed root. Emitting the base URL's own path here
// would nest the tree one full prefix too deep — `updates/cli-bundle/…`
// underneath a directory already served at `/updates/` — and every
// manifest URL the index advertises would 404.
//
// So the base path is subtracted rather than assumed empty, which is what the
// original code did when the feed lived at the root of its own hostname.
function relativeFeedPath (manifest) {
  const root = updateFeedRoot({
    baseUrl: SDN_UPDATE_FEED_BASE_URL,
    channel: manifest.channel,
    platform: manifest.target.platform,
    arch: manifest.target.arch,
    kind: manifest.target.kind
  })
  const full = new URL(root).pathname
  const base = SDN_UPDATE_FEED_BASE_PATH
  const relative = base && full.startsWith(`${base}/`) ? full.slice(base.length) : full
  return relative.replace(/^\//, '')
}

async function buildFeedFromFiles ({
  generatedAt = new Date().toISOString(),
  outDir,
  entries
}) {
  if (!outDir) {
    throw new Error('missing output directory')
  }
  if (!Array.isArray(entries) || entries.length === 0) {
    throw new Error('missing update feed entries')
  }

  const manifests = []
  const copiedEntries = []
  const manifestsByFeedPath = new Map()

  for (const entry of entries) {
    const manifest = await readJson(entry.manifestPath)
    manifests.push(manifest)

    const feedPath = relativeFeedPath(manifest)
    const feedManifests = manifestsByFeedPath.get(feedPath) || []
    feedManifests.push(manifest)
    manifestsByFeedPath.set(feedPath, feedManifests)

    const versionDir = path.join(outDir, feedPath, manifest.version)
    const manifestOutPath = path.join(versionDir, 'manifest.json')
    const carrierOutPath = path.join(versionDir, 'update.wasm')

    await fs.mkdir(versionDir, { recursive: true })
    await fs.copyFile(entry.manifestPath, manifestOutPath)
    await fs.copyFile(entry.carrierPath, carrierOutPath)

    copiedEntries.push({
      updateId: manifest.update_id,
      version: manifest.version,
      manifestPath: manifestOutPath,
      carrierPath: carrierOutPath
    })
  }

  const indexPaths = []
  for (const [feedPath, feedManifests] of manifestsByFeedPath.entries()) {
    const index = buildReleaseIndex({ generatedAt, manifests: feedManifests })
    const indexPath = path.join(outDir, feedPath, 'index.json')
    await writeJson(indexPath, index)
    indexPaths.push(indexPath)
  }

  return {
    indexPath: indexPaths[0],
    indexPaths,
    entries: copiedEntries
  }
}

function parseArgs (argv) {
  const config = {
    entries: []
  }

  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i]
    if (arg === '--out-dir') {
      config.outDir = argv[++i]
    } else if (arg === '--generated-at') {
      config.generatedAt = argv[++i]
    } else if (arg === '--entry') {
      const [manifestPath, carrierPath] = String(argv[++i]).split(':')
      config.entries.push({ manifestPath, carrierPath })
    } else {
      throw new Error(`unknown argument: ${arg}`)
    }
  }

  return config
}

if (require.main === module) {
  buildFeedFromFiles(parseArgs(process.argv.slice(2)))
    .then(result => {
      process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)
    })
    .catch(err => {
      process.stderr.write(`${err.stack || err.message}\n`)
      process.exitCode = 1
    })
}

module.exports = {
  buildFeedFromFiles,
  parseArgs
}
