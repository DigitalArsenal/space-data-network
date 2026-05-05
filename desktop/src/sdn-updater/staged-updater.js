const fs = require('fs-extra')
const path = require('path')
const { verifyDownloadedUpdatePayload } = require('./manifest')

function updatePaths (rootDir, updateId) {
  return {
    current: path.join(rootDir, 'current'),
    failed: path.join(rootDir, 'failed', updateId),
    rollback: path.join(rootDir, 'rollback', updateId),
    staged: path.join(rootDir, 'staged', updateId)
  }
}

async function replacePath (from, to) {
  await fs.remove(to)
  await fs.move(from, to, { overwrite: false })
}

async function daemonWasRunning (lifecycle) {
  if (!lifecycle?.getIpfsd) {
    return false
  }

  return Boolean(await lifecycle.getIpfsd(true))
}

async function stopDaemonIfNeeded (lifecycle, wasRunning) {
  if (wasRunning && lifecycle?.stopIpfs) {
    await lifecycle.stopIpfs()
  }
}

async function startDaemonIfNeeded (lifecycle, wasRunning) {
  if (wasRunning && lifecycle?.startIpfs) {
    await lifecycle.startIpfs()
  }
}

function createStagedUpdater ({ rootDir }) {
  async function downloadVerifyAndStageUpdate ({
    manifestUrl,
    carrierUrl,
    fetchJson,
    fetchBytes,
    extractBundleBytes,
    verifyOptions
  }) {
    const manifest = await fetchJson(manifestUrl)
    const wasmBytes = await fetchBytes(carrierUrl)
    const bundleBytes = await extractBundleBytes(wasmBytes)
    const verified = verifyDownloadedUpdatePayload({
      manifest,
      wasmBytes,
      bundleBytes,
      ...verifyOptions
    })
    const paths = updatePaths(rootDir, verified.updateId)

    await fs.remove(paths.staged)
    await fs.ensureDir(paths.staged)
    await fs.writeJson(path.join(paths.staged, 'manifest.json'), manifest, { spaces: 2 })
    await fs.writeFile(path.join(paths.staged, 'update.wasm'), wasmBytes)
    await fs.writeFile(path.join(paths.staged, 'bundle.tar.zst'), bundleBytes)
    await fs.writeJson(path.join(paths.staged, 'verified.json'), {
      ...verified,
      manifestUrl,
      carrierUrl,
      stagedAt: new Date().toISOString()
    }, { spaces: 2 })

    return {
      ...verified,
      stagedPath: paths.staged
    }
  }

  async function commitStagedUpdate ({ updateId, lifecycle, healthCheck = async () => {} }) {
    const paths = updatePaths(rootDir, updateId)

    if (!await fs.pathExists(paths.staged)) {
      throw new Error('missing staged update')
    }

    const wasRunning = await daemonWasRunning(lifecycle)
    await stopDaemonIfNeeded(lifecycle, wasRunning)

    try {
      await fs.remove(paths.rollback)
      if (await fs.pathExists(paths.current)) {
        await fs.move(paths.current, paths.rollback, { overwrite: false })
      }

      await replacePath(paths.staged, paths.current)
      await healthCheck(paths.current)
      await startDaemonIfNeeded(lifecycle, wasRunning)

      return {
        updateId,
        currentPath: paths.current,
        rollbackPath: paths.rollback,
        rolledBack: false
      }
    } catch (err) {
      await fs.remove(paths.failed)
      if (await fs.pathExists(paths.current)) {
        await fs.move(paths.current, paths.failed, { overwrite: false })
      }
      if (await fs.pathExists(paths.rollback)) {
        await replacePath(paths.rollback, paths.current)
      }
      await startDaemonIfNeeded(lifecycle, wasRunning)
      throw err
    }
  }

  return {
    commitStagedUpdate,
    downloadVerifyAndStageUpdate
  }
}

module.exports = {
  createStagedUpdater
}
