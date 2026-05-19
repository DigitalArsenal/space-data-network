import React, { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import VersionLink from '../../../../../../webui/src/components/version-link/version-link.jsx'
import { Definition, DefinitionList } from '../../../../../../webui/src/components/definition/Definition.js'

function normalizeNodeInfo(payload) {
  const peerId = pickString(payload, ['peer_id', 'peerId', 'ID'])
  const agentVersion = normalizeAgentVersion(payload)
  return { peerId, agentVersion }
}

function normalizeAgentVersion(payload) {
  const raw = pickString(payload, ['agent_version', 'agentVersion', 'AgentVersion', 'version'])
  if (!raw) {
    return null
  }
  return raw.includes('/') ? raw : `spacedatanetwork/${raw}`
}

function pickString(payload, keys) {
  if (!payload || typeof payload !== 'object') {
    return null
  }
  for (const key of keys) {
    const value = payload[key]
    if (typeof value === 'string' && value.trim()) {
      return value.trim()
    }
  }
  return null
}

function httpUrlFromKuboApiAddress(value) {
  const raw = String(value ?? '').trim()
  if (!raw) {
    return null
  }

  if (/^https?:\/\//.test(raw)) {
    return raw.replace(/\/$/, '')
  }

  const match = raw.match(/^\/ip4\/([^/]+)\/tcp\/(\d+)/)
  if (match) {
    return `http://${match[1]}:${match[2]}`
  }

  return null
}

function readKuboApiBaseUrl() {
  if (typeof window === 'undefined') {
    return null
  }

  const fromUrl = new URL(window.location.href).searchParams.get('api')
  const fromStorage = window.localStorage?.getItem('ipfsApi')
  return httpUrlFromKuboApiAddress(fromUrl) ?? httpUrlFromKuboApiAddress(fromStorage) ?? 'http://127.0.0.1:5001'
}

async function loadSdnNodeInfo() {
  const response = await fetch('/api/node/info', {
    credentials: 'include'
  })
  if (!response.ok) {
    return null
  }
  return normalizeNodeInfo(await response.json())
}

async function loadKuboRpcNodeInfo() {
  const baseUrl = readKuboApiBaseUrl()
  if (!baseUrl) {
    return null
  }

  const kuboRpcIdentityUrl = `${baseUrl}/api/v0/id`
  const response = await fetch(kuboRpcIdentityUrl, {
    method: 'POST'
  })
  if (!response.ok) {
    return null
  }
  return normalizeNodeInfo(await response.json())
}

const NodeInfo = () => {
  const { t } = useTranslation('app')
  const [nodeInfo, setNodeInfo] = useState({ peerId: null, agentVersion: null })

  useEffect(() => {
    let cancelled = false

    async function loadNodeInfo() {
      try {
        const loadedNodeInfo = await loadSdnNodeInfo() ?? await loadKuboRpcNodeInfo()
        if (!cancelled) {
          setNodeInfo(loadedNodeInfo ?? { peerId: null, agentVersion: null })
        }
      } catch {
        // Leave the loading fallback in place if the SDN node info request fails.
      }
    }

    loadNodeInfo()

    return () => {
      cancelled = true
    }
  }, [])

  return (
    <DefinitionList>
      <Definition term={t('terms.peerId')} desc={nodeInfo.peerId ?? t('loading')} />
      <Definition term={t('terms.agent')} desc={<VersionLink agentVersion={nodeInfo.agentVersion ?? t('loading')} />} />
      <Definition term={t('terms.ui')} desc={process.env.REACT_APP_GIT_REV} />
    </DefinitionList>
  )
}

export default NodeInfo
