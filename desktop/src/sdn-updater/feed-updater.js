// SDN feed updater. Composes the SDN-owned release feed, the inert WASM
// carrier parser, and the staged updater into the full desktop update check
// flow described in docs/sdn-signed-updater.md. Pure and dependency-injected:
// network access goes through the provided fetchJson/fetchBytes, daemon
// lifecycle through the provided lifecycle object.

const { extractBundleBytes } = require('./carrier')
const {
  SDN_UPDATE_FEED_BASE_URL,
  assertSdnOwnedDesktopUpdateFeedUrl,
  updateFeedRoot
} = require('./release-feed')
const { createStagedUpdater } = require('./staged-updater')
const { hasTrustedUpdateRoots } = require('./trusted-roots')

const SDN_UPDATE_INDEX_SCHEMA = 'org.spacedatanetwork.update.index.v1'
const DESKTOP_APP_KIND = 'desktop-app'

function createSdnFeedUpdater ({
  rootDir,
  baseUrl = SDN_UPDATE_FEED_BASE_URL,
  channel,
  platform,
  arch,
  currentSequence,
  trustedRoots,
  fetchJson,
  fetchBytes,
  lifecycle,
  log = () => {},
  now
}) {
  const stagedUpdater = createStagedUpdater({ rootDir })

  function isCandidateEntry (entry) {
    return Boolean(entry) &&
      entry.target?.kind === DESKTOP_APP_KIND &&
      entry.target?.platform === platform &&
      entry.target?.arch === arch &&
      entry.channel === channel &&
      Number.isInteger(entry.sequence) &&
      entry.sequence > currentSequence
  }

  function selectUpdateEntry (index) {
    if (!index || index.schema !== SDN_UPDATE_INDEX_SCHEMA) {
      throw new Error('unsupported update index schema')
    }
    if (!Array.isArray(index.updates)) {
      throw new Error('missing update index entries')
    }

    return index.updates
      .filter(isCandidateEntry)
      .sort((a, b) => b.sequence - a.sequence)[0] || null
  }

  async function checkAndStage () {
    if (!hasTrustedUpdateRoots(trustedRoots)) {
      log('no trusted SDN update roots configured, refusing to check for updates')
      return null
    }

    const feedUrl = updateFeedRoot({ baseUrl, channel, platform, arch, kind: DESKTOP_APP_KIND })
    const indexUrl = `${feedUrl}/index.json`
    const index = await fetchJson(indexUrl)
    const entry = selectUpdateEntry(index)

    if (!entry) {
      log(`no SDN desktop update newer than sequence ${currentSequence} in ${indexUrl}`)
      return null
    }

    assertSdnOwnedDesktopUpdateFeedUrl(entry.manifest_url)
    assertSdnOwnedDesktopUpdateFeedUrl(entry.carrier_url)

    const staged = await stagedUpdater.downloadVerifyAndStageUpdate({
      manifestUrl: entry.manifest_url,
      carrierUrl: entry.carrier_url,
      fetchJson,
      fetchBytes,
      extractBundleBytes,
      verifyOptions: {
        platform,
        arch,
        currentSequence,
        trustedRoots,
        ...(now ? { now } : {})
      }
    })

    return {
      ...staged,
      version: entry.version,
      channel: entry.channel
    }
  }

  async function commit (updateId, healthCheck) {
    return stagedUpdater.commitStagedUpdate({
      updateId,
      lifecycle,
      ...(healthCheck ? { healthCheck } : {})
    })
  }

  return {
    checkAndStage,
    commit
  }
}

module.exports = {
  SDN_UPDATE_INDEX_SCHEMA,
  createSdnFeedUpdater
}
