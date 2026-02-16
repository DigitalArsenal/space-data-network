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
import WalletPage from '../wallet/WalletPage.js'

const PeersTabs = ({ active, onChange, isAdmin }) => {
  const tabs = [
    { id: 'network', label: 'Network' },
    ...(isAdmin
      ? [
          { id: 'trust', label: 'Trust' },
          { id: 'wallet', label: 'Wallet' }
        ]
      : []
    )
  ]
  return (
    <div className='flex flex-wrap' style={{ gap: 8, marginBottom: 16 }}>
      {tabs.map(t => (
        <button
          key={t.id}
          className='pointer'
          onClick={() => onChange(t.id)}
          style={{
            padding: '8px 10px',
            borderRadius: 10,
            border: `1px solid ${active === t.id ? 'rgba(88, 166, 255, 0.7)' : 'var(--sdn-border)'}`,
            background: active === t.id ? 'rgba(88, 166, 255, 0.10)' : 'var(--sdn-bg-tertiary)',
            color: 'var(--sdn-text-primary)',
            fontWeight: 700,
            fontFamily: 'Montserrat, sans-serif',
            letterSpacing: '0.02em'
          }}
        >
          {t.label}
        </button>
      ))}
    </div>
  )
}

const PeersPage = ({ t, toursEnabled, handleJoyrideCallback, isIpfsContext, isAdminUser: isAdmin }) => {
  const [tab, setTab] = useState('network')

  return (
    <div data-id='PeersPage' className='overflow-hidden'>
      <Helmet>
        <title>{t('title')} | SDN</title>
      </Helmet>

      <PeersTabs active={tab} onChange={setTab} isAdmin={isAdmin} />

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

      {tab === 'trust' && isAdmin && <TrustPage embedded />}
      {tab === 'wallet' && isAdmin && <WalletPage embedded />}

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
  withTour(withTranslation('peers')(PeersPage))
)
