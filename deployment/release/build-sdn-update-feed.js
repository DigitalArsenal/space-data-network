#!/usr/bin/env node

const fs = require('fs/promises')
const path = require('path')
const {
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

function relativeFeedPath (manifest) {
  const root = updateFeedRoot({
    baseUrl: 'https://updates.spacedatanetwork.org',
    channel: manifest.channel,
    platform: manifest.target.platform,
    arch: manifest.target.arch,
    kind: manifest.target.kind
  })
  return new URL(root).pathname.replace(/^\//, '')
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
