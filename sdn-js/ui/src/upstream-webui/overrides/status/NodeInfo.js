import React, { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import VersionLink from '../../../../../../webui/src/components/version-link/version-link.jsx'
import { Definition, DefinitionList } from '../../../../../../webui/src/components/definition/Definition.js'

function normalizeNodeInfo(payload) {
  const peerId = pickString(payload, ['peer_id', 'peerId'])
  const agentVersion = normalizeAgentVersion(payload)
  return { peerId, agentVersion }
}

function normalizeAgentVersion(payload) {
  const raw = pickString(payload, ['agent_version', 'agentVersion', 'version'])
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

const NodeInfo = () => {
  const { t } = useTranslation('app')
  const [nodeInfo, setNodeInfo] = useState({ peerId: null, agentVersion: null })

  useEffect(() => {
    let cancelled = false

    async function loadNodeInfo() {
      try {
        const response = await fetch('/api/node/info', {
          credentials: 'include'
        })
        if (!response.ok) {
          return
        }
        const payload = await response.json()
        if (!cancelled) {
          setNodeInfo(normalizeNodeInfo(payload))
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
