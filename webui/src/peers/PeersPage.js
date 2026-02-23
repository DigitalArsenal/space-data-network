import React, { useState } from 'react'
import { connect } from 'redux-bundler-react'
import { Helmet } from 'react-helmet'
import { withTranslation } from 'react-i18next'
import ReactJoyride from 'react-joyride'
import withTour from '../components/tour/withTour.js'
import { peersTour } from '../lib/tours.js'
import { getJoyrideLocales } from '../helpers/i8n.js'

// Components
import Box from '../components/box/Box.js'
import WorldMap from './WorldMap/WorldMap.js'
import PeersTable from './PeersTable/PeersTable.js'
import SdnPeersPanel from './SdnPeersPanel.js'
import AddConnection from './AddConnection/AddConnection.js'
import CliTutorMode from '../components/cli-tutor-mode/CliTutorMode.js'
import { cliCmdKeys, cliCommandList } from '../bundles/files/consts.js'
import TrustPage from '../trust/TrustPage.js'
import IdentityCard from '../components/identity-card/IdentityCard.js'
import EPMEditor from '../components/epm-editor/EPMEditor.js'
import PeerDirectory from './PeerDirectory.js'
import GraphTab from './graph/GraphTab.js'
import './PeersPage.css'

const PeersTabs = ({ active, onChange, isAdmin }) => {
  const tabs = [
    { id: 'network', label: 'Network' },
    { id: 'identity', label: 'Identity' },
    { id: 'directory', label: 'Directory' },
    { id: 'graph', label: 'Graph' },
    ...(isAdmin
      ? [
          { id: 'trust', label: 'Trust' }
        ]
      : []
    )
  ]
  return (
    <div className='peers-tabs'>
      {tabs.map(t => (
        <button
          key={t.id}
          className={`peers-tab${active === t.id ? ' peers-tab-active' : ''}`}
          onClick={() => onChange(t.id)}
        >
          {t.label}
        </button>
      ))}
    </div>
  )
}

const PeersPage = ({
  t, toursEnabled, handleJoyrideCallback, isIpfsContext, isAdminUser: isAdmin,
  nodeEpm, nodeQrUrl, epmLoading,
  doFetchNodeEPM, doFetchNodeQR, doUpdateNodeProfile, doFetchNodeVCard
}) => {
  const [tab, setTab] = useState('network')
  const [epmSaving, setEpmSaving] = useState(false)

  React.useEffect(() => {
    if (tab === 'identity' && !nodeEpm) {
      doFetchNodeEPM()
    }
  }, [tab, nodeEpm, doFetchNodeEPM])

  const handleSaveProfile = async (profile) => {
    setEpmSaving(true)
    try {
      await doUpdateNodeProfile(profile)
      doFetchNodeEPM()
    } finally {
      setEpmSaving(false)
    }
  }

  const handleShowQR = () => {
    if (!nodeQrUrl) doFetchNodeQR()
  }

  const handleDownloadVCard = async () => {
    try {
      const res = await fetch('/api/node/epm/vcard', { credentials: 'same-origin' })
      if (!res.ok) return
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `${nodeEpm?.dn || 'node'}.vcf`
      a.click()
      URL.revokeObjectURL(url)
    } catch (err) {
      console.error('Failed to download vCard:', err)
    }
  }

  return (
    <div data-id='PeersPage' className='peers-page'>
      <Helmet>
        <title>{t('title')} | SDN</title>
      </Helmet>

      <div className='peers-page-header'>
        <PeersTabs active={tab} onChange={setTab} isAdmin={isAdmin} />
      </div>

      <div className='peers-page-body'>
        {tab === 'network' && (
          <>
            <div className='flex justify-end items-center mb3'>
              <CliTutorMode showIcon={true} command={cliCommandList[cliCmdKeys.ADD_NEW_PEER]()} t={t}/>
              <AddConnection />
            </div>

            <SdnPeersPanel />

            {isIpfsContext && (
              <Box className='pt3 ph3 pb4'>
                <WorldMap className='joyride-peers-map' />
                <PeersTable className='joyride-peers-table' />
              </Box>
            )}
          </>
        )}

        {tab === 'identity' && (
          <div className='peers-identity-layout'>
            {epmLoading && !nodeEpm
              ? <div style={{ color: 'var(--sdn-text-secondary)', padding: 20 }}>Loading identity...</div>
              : (
                  <>
                    <IdentityCard
                      epm={nodeEpm}
                      qrUrl={nodeQrUrl}
                      onDownloadVCard={handleDownloadVCard}
                      onShowQR={handleShowQR}
                      isLocal
                    />
                    <EPMEditor
                      epm={nodeEpm}
                      onSave={handleSaveProfile}
                      saving={epmSaving}
                    />
                  </>
                )
            }
          </div>
        )}

        {tab === 'directory' && <PeerDirectory />}

        {tab === 'graph' && <GraphTab />}

        {tab === 'trust' && isAdmin && <TrustPage embedded />}
      </div>

      <ReactJoyride
        run={toursEnabled}
        steps={peersTour.getSteps({ t })}
        styles={peersTour.styles}
        callback={handleJoyrideCallback}
        continuous
        scrollToFirstStep
        locale={getJoyrideLocales(t)}
        showProgress />
    </div>
  )
}

export default connect(
  'selectToursEnabled',
  'selectIsCliTutorModeEnabled',
  'selectIsIpfsContext',
  'selectIsAdminUser',
  'selectNodeEpm',
  'selectNodeQrUrl',
  'selectEpmLoading',
  'doFetchNodeEPM',
  'doFetchNodeQR',
  'doUpdateNodeProfile',
  'doFetchNodeVCard',
  withTour(withTranslation('peers')(PeersPage))
)
