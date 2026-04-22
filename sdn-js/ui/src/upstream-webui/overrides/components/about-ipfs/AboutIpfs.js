import React from 'react'
import { withTranslation } from 'react-i18next'
import Box from '../../../../../../../webui/src/components/box/Box.js'

/**
 * SDN root keeps the upstream welcome layout and replaces the explainer copy.
 *
 * Synced base: webui/src/components/about-ipfs/AboutIpfs.js
 */
export const AboutIpfs = () => {
  return (
    <Box>
      <h2 className='mt0 mb3 montserrat fw2 f3 charcoal'>What is Space Data Network?</h2>
      <ul className='pl3'>
        <li className='mb2'><strong>A peer-to-peer network for space situational awareness data</strong> that lets organizations exchange standardized observations, conjunction context, and mission tooling without a central broker</li>
        <li className='mb2'><strong>Built on proven distributed systems</strong> including <a className='link blue' target='_blank' rel='noopener noreferrer' href='https://ipfs.tech/'>IPFS</a>, <a className='link blue' target='_blank' rel='noopener noreferrer' href='https://libp2p.io/'>libp2p</a>, and versioned schemas from <a className='link blue' target='_blank' rel='noopener noreferrer' href='https://spacedatastandards.org/'>Space Data Standards</a></li>
        <li className='mb2'><strong>One suite across browser, desktop, and server</strong> so the same SDN dashboard can control any node your wallet identity is allowed to access</li>
        <li className='mb2'><strong>Full IPFS compatibility remains available</strong> through the untouched upstream dashboard at <a className='link blue' href='/webui/'>/webui</a> whenever you need raw Kubo and IPFS WebUI capabilities</li>
        <li className='mb2'><strong>Designed for encrypted delivery and standards-aware applications</strong> so nodes can advertise capabilities, move protected bundles, and load modules tied to pinned suite versions</li>
      </ul>
    </Box>
  )
}

export default withTranslation('welcome')(AboutIpfs)
