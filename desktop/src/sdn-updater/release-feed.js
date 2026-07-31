// OWNER RULING 2026-07-30 (graph/tasks/sdn-signed-updater.md): the update
// server RUNS ON sdn.spaceaware.io. updates.spacedatanetwork.org is MOOT — it
// was never allocated, and its unobtainable DNS/HTTPS is what parked this task
// from 2026-05 until the ruling. Do not resurrect it.
//
// The path prefix is part of the base URL because host-01 has no web server in
// front of the daemon: the SDN daemon itself owns :443, and it serves the feed
// from its own API namespace (sdn-server/internal/api/update_feed.go mounts
// /api/v1/updates/). A bare-origin feed would have to claim top-level paths on
// the same origin that serves the node dashboard.
const SDN_UPDATE_FEED_BASE_URL = 'https://sdn.spaceaware.io/api/v1/updates'
const SDN_UPDATE_FEED_ORIGIN = new URL(SDN_UPDATE_FEED_BASE_URL)
const SDN_UPDATE_FEED_HOSTNAME = SDN_UPDATE_FEED_ORIGIN.hostname
// Normalized with no trailing slash so it can be prefix-matched against a
// request path without an off-by-one at the boundary.
const SDN_UPDATE_FEED_BASE_PATH = SDN_UPDATE_FEED_ORIGIN.pathname.replace(/\/$/, '')

function normalizeBaseUrl (baseUrl = SDN_UPDATE_FEED_BASE_URL) {
  const parsed = new URL(baseUrl)
  if (parsed.protocol !== 'https:') {
    throw new Error('SDN update feed must use HTTPS')
  }
  return parsed.toString().replace(/\/$/, '')
}

function assertPathSegment (value, name) {
  if (typeof value !== 'string' || !/^[a-zA-Z0-9._-]+$/.test(value)) {
    throw new Error(`invalid update feed ${name}`)
  }
}

function assertManifestShape (manifest) {
  if (!manifest || typeof manifest !== 'object') {
    throw new Error('missing update manifest')
  }
  assertPathSegment(manifest.channel, 'channel')
  assertPathSegment(manifest.version, 'version')
  assertPathSegment(manifest.target?.platform, 'platform')
  assertPathSegment(manifest.target?.arch, 'arch')
  assertPathSegment(manifest.target?.kind, 'kind')
}

function updateFeedRoot ({ baseUrl = SDN_UPDATE_FEED_BASE_URL, channel, platform, arch, kind }) {
  assertPathSegment(channel, 'channel')
  assertPathSegment(platform, 'platform')
  assertPathSegment(arch, 'arch')

  const productPath = kind === 'desktop-app' ? 'desktop' : kind
  assertPathSegment(productPath, 'kind')
  return `${normalizeBaseUrl(baseUrl)}/${productPath}/${channel}/${platform}/${arch}`
}

function desktopAppFeedUrls ({ baseUrl = SDN_UPDATE_FEED_BASE_URL, channel, platform, arch, version }) {
  return updatePayloadFeedUrls({
    baseUrl,
    channel,
    platform,
    arch,
    kind: 'desktop-app',
    version
  })
}

function updatePayloadFeedUrls ({ baseUrl = SDN_UPDATE_FEED_BASE_URL, channel, platform, arch, kind, version }) {
  assertPathSegment(version, 'version')
  const normalizedBaseUrl = normalizeBaseUrl(baseUrl)
  const feedUrl = updateFeedRoot({
    baseUrl: normalizedBaseUrl,
    channel,
    platform,
    arch,
    kind
  })

  return {
    baseUrl: normalizedBaseUrl,
    feedUrl,
    indexUrl: `${feedUrl}/index.json`,
    manifestUrl: `${feedUrl}/${version}/manifest.json`,
    carrierUrl: `${feedUrl}/${version}/update.wasm`
  }
}

// The inherited IPFS Desktop updater would happily take a GitHub release feed;
// this assertion is what keeps SDN Desktop pinned to SDN-owned metadata. It
// checks the feed PATH as well as the origin because sdn.spaceaware.io now
// serves the node dashboard and API from the same origin — origin alone no
// longer distinguishes "the update feed" from "anything else this host serves".
function assertSdnOwnedDesktopUpdateFeedUrl (feedUrl) {
  const parsed = new URL(feedUrl)
  if (parsed.protocol !== 'https:' || parsed.hostname !== SDN_UPDATE_FEED_HOSTNAME) {
    throw new Error('SDN desktop updates must use the SDN update feed origin')
  }
  if (!parsed.pathname.startsWith(`${SDN_UPDATE_FEED_BASE_PATH}/desktop/`)) {
    throw new Error('SDN desktop updates must use the desktop update feed path')
  }
}

function releaseIndexEntry (manifest, baseUrl) {
  assertManifestShape(manifest)
  const urls = updatePayloadFeedUrls({
    baseUrl,
    channel: manifest.channel,
    platform: manifest.target.platform,
    arch: manifest.target.arch,
    kind: manifest.target.kind,
    version: manifest.version
  })

  return {
    update_id: manifest.update_id,
    version: manifest.version,
    sequence: manifest.sequence,
    channel: manifest.channel,
    target: {
      platform: manifest.target.platform,
      arch: manifest.target.arch,
      kind: manifest.target.kind
    },
    expires_at: manifest.expires_at,
    bundle_hash: manifest.bundle?.hash,
    wasm_hash: manifest.wasm?.hash,
    signing_key_id: manifest.signing?.key_id,
    manifest_url: urls.manifestUrl,
    carrier_url: urls.carrierUrl
  }
}

function buildReleaseIndex ({
  baseUrl = SDN_UPDATE_FEED_BASE_URL,
  generatedAt = new Date().toISOString(),
  manifests
}) {
  if (!Array.isArray(manifests) || manifests.length === 0) {
    throw new Error('missing update manifests')
  }

  const normalizedBaseUrl = normalizeBaseUrl(baseUrl)
  return {
    schema: 'org.spacedatanetwork.update.index.v1',
    generated_at: generatedAt,
    feed_base_url: normalizedBaseUrl,
    updates: manifests
      .map(manifest => releaseIndexEntry(manifest, normalizedBaseUrl))
      .sort((a, b) => b.sequence - a.sequence)
  }
}

module.exports = {
  SDN_UPDATE_FEED_BASE_URL,
  SDN_UPDATE_FEED_BASE_PATH,
  SDN_UPDATE_FEED_HOSTNAME,
  assertSdnOwnedDesktopUpdateFeedUrl,
  buildReleaseIndex,
  desktopAppFeedUrls,
  normalizeBaseUrl,
  updatePayloadFeedUrls,
  updateFeedRoot
}
