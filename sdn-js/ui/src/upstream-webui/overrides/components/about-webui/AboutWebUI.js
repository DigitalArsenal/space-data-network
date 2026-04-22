import React from 'react'
import { withTranslation } from 'react-i18next'
import Box from '../../../../../../../webui/src/components/box/Box.js'

/**
 * SDN root keeps the upstream welcome layout and replaces the product intro.
 *
 * Synced base: webui/src/components/about-webui/AboutWebUI.js
 */
export const AboutWebUI = () => {
  return (
    <Box>
      <h2 className='mt0 mb3 montserrat fw2 f3 charcoal'>In this app, you can …</h2>
      <ul className='pl3'>
        <li className='mb2'><a href='#/' className='link blue u b'>Check your node status</a>, including SDN connectivity, storage, bandwidth, and the health of the current node</li>
        <li className='mb2'><a href='#/files' className='link blue u b'>Browse and manage data</a> in the node, including content addressed files, IPLD-backed structures, and imported assets</li>
        <li className='mb2'><a href='#/explore' className='link blue b'>Inspect linked data</a> and explore the underlying content graph that SDN builds on top of IPFS primitives</li>
        <li className='mb2'><a href='#/peers' className='link blue b'>See observed SDN peers</a> so the main dashboard only reflects nodes participating in the Space Data Network</li>
        <li className='mb2'><a href='/webui' className='link blue b'>Open the full IPFS dashboard</a> whenever you need the unmodified upstream WebUI and all of its current capabilities</li>
        <li className='f5'><a href='https://github.com/DigitalArsenal/space-data-network' className='link blue b' target='_blank' rel='noopener noreferrer'>Review the SDN source</a>, track suite changes, or report an issue as the dashboard evolves alongside upstream IPFS</li>
      </ul>
    </Box>
  )
}

export default withTranslation('welcome')(AboutWebUI)
