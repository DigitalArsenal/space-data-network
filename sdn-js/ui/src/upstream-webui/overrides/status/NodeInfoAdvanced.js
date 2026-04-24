import React, { useEffect, useMemo, useRef, useState } from 'react'
import { multiaddr } from '@multiformats/multiaddr'
import { connect } from 'redux-bundler-react'
import { withTranslation } from 'react-i18next'
import { useIdentity } from '../../../../../../webui/src/contexts/identity-context.jsx'
import Address from '../../../../../../webui/src/components/address/Address.js'
import Details from '../../../../../../webui/src/components/details/Details.js'
import ProviderLink from '../../../../../../webui/src/components/provider-link/ProviderLink.js'
import { Definition, DefinitionList } from '../../../../../../webui/src/components/definition/Definition.js'
import { getSharedUiRuntimeAdapter } from '../../../../../src/ui/runtime/server-adapter.js'

function isMultiaddr(addr) {
  try {
    multiaddr(addr)
    return true
  } catch (_) {
    return false
  }
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

function normalizeNodeProfile(context) {
  return {
    displayName: pickString(context, ['displayName', 'display_name', 'name']),
    peerId: pickString(context, ['peerId', 'peer_id']),
    transport: pickString(context, ['transport']),
    descriptorUrl: pickString(context, ['descriptorUrl', 'descriptor_url']),
  }
}

const NodeInfoAdvanced = ({ t, ipfsProvider, ipfsApiAddress, gatewayUrl, isNodeInfoOpen, doSetIsNodeInfoOpen }) => {
  const runtimeRef = useRef(null)
  const { identity, isLoading } = useIdentity()
  const loadingString = t('loading')
  const [nodeProfile, setNodeProfile] = useState({
    displayName: null,
    peerId: null,
    transport: null,
    descriptorUrl: null,
  })
  const [nodeProfileStatus, setNodeProfileStatus] = useState('idle')
  const [nodeProfileError, setNodeProfileError] = useState(null)

  if (!runtimeRef.current) {
    runtimeRef.current = getSharedUiRuntimeAdapter()
  }

  useEffect(() => {
    if (!isNodeInfoOpen) {
      return undefined
    }

    let cancelled = false

    async function loadNodeProfile() {
      setNodeProfileStatus('loading')
      setNodeProfileError(null)

      try {
        const connected = await runtimeRef.current.connect()
        if (!cancelled) {
          setNodeProfile(normalizeNodeProfile(connected.nodeContext))
          setNodeProfileStatus('ready')
        }
      } catch (err) {
        if (!cancelled) {
          setNodeProfileError(err instanceof Error ? err.message : String(err))
          setNodeProfileStatus('error')
        }
      }
    }

    loadNodeProfile()

    return () => {
      cancelled = true
    }
  }, [isNodeInfoOpen])

  const addressComponent = useMemo(() => {
    if (isLoading || identity?.addresses == null) return loadingString
    return [...new Set(identity.addresses)].sort().map(addr => <Address key={addr} value={addr} />)
  }, [identity?.addresses, isLoading, loadingString])

  const publicKeyComponent = useMemo(() => {
    if (isLoading) return loadingString
    return identity?.publicKey ?? null
  }, [identity?.publicKey, isLoading, loadingString])

  const handleSummaryClick = (ev) => {
    doSetIsNodeInfoOpen(!isNodeInfoOpen)
    ev.preventDefault()
  }

  const asAPIString = (value) => {
    // hide raw JSON if advanced config is present in the string
    return typeof value !== 'string'
      ? t('customApiConfig')
      : value
  }

  const nodeProfileDescription = nodeProfileStatus === 'loading'
    ? loadingString
    : 'Node identity loaded from the SDN runtime adapter.'

  return (
    <Details className='mt3 f6' summaryText={t('app:terms.advanced')} open={isNodeInfoOpen} onClick={handleSummaryClick}>
      <DefinitionList className='mt3'>
        <Definition advanced term={t('app:terms.gateway')} desc={gatewayUrl} />
        {ipfsProvider === 'httpClient'
          ? <Definition advanced term={t('app:terms.api')} desc={
            (<div id='http-api-address' className='flex items-center'>
              {isMultiaddr(ipfsApiAddress)
                ? (<Address value={ipfsApiAddress} />)
                : asAPIString(ipfsApiAddress)
              }
              <a className='ml2 link blue sans-serif fw6' href='#/settings'>{t('app:actions.edit')}</a>
            </div>)
          } />
          : <Definition advanced term={t('app:terms.api')} desc={<ProviderLink name={ipfsProvider} />} />
        }
        <Definition advanced term={t('app:terms.addresses')} desc={addressComponent} />
        <Definition advanced term={t('app:terms.publicKey')} desc={publicKeyComponent} />
      </DefinitionList>

      <div className='mt3 pt3 bt b--black-10'>
        <h2 className='f6 ttu tracked black-60 mt0 mb2'>Node profile</h2>
        <DefinitionList>
          <Definition advanced term='Display name' desc={nodeProfile.displayName ?? 'Unavailable'} termWidth={130} />
          <Definition advanced term='Peer ID' desc={nodeProfile.peerId ?? 'Unavailable'} termWidth={130} />
          <Definition advanced term='Transport' desc={nodeProfile.transport ?? 'Unavailable'} termWidth={130} />
          <Definition advanced term='Descriptor URL' desc={nodeProfile.descriptorUrl ?? 'Unavailable'} termWidth={130} />
        </DefinitionList>
        <div className='mt2 silver'>
          {nodeProfileError ?? nodeProfileDescription}
        </div>
      </div>
    </Details>
  )
}

export default connect(
  'selectIpfsProvider',
  'selectIpfsApiAddress',
  'selectGatewayUrl',
  'selectIsNodeInfoOpen',
  'doSetIsNodeInfoOpen',
  withTranslation('status')(NodeInfoAdvanced)
)
