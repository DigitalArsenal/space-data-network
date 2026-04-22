import React from 'react'
import { withTranslation } from 'react-i18next'
import Box from '../../../../../../../webui/src/components/box/Box.js'
import GlyphTick from '../../../../../../../webui/src/icons/GlyphTick.js'

/**
 * SDN root keeps the upstream welcome hero layout and swaps the copy.
 *
 * Synced base: webui/src/components/is-connected/IsConnected.js
 */
export const IsConnected = () => {
  return (
    <Box className='pv3 ph4'>
      <div>
        <div className='flex flex-wrap items-center'>
          <GlyphTick style={{ height: 76 }} className='fill-green' role='presentation' />
          <h1 className='montserrat fw4 charcoal ma0 f3 green'>Connected to the Space Data Network</h1>
        </div>
        <p className='fw6 mt1 ml3-ns w-100'>This node is online and participating in the SDN suite for distributed space data, standards-aware tools, and authenticated control surfaces.</p>
      </div>
    </Box>
  )
}

export default withTranslation('welcome')(IsConnected)
