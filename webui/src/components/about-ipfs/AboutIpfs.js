import React from 'react'
import { withTranslation, Trans } from 'react-i18next'
import Box from '../box/Box.js'

/**
 * @param {Object} props
 * @param {import('i18next').TFunction} props.t
 */
export const AboutIpfs = ({ t }) => {
  return (
    <Box>
      <h2 className='mt0 mb3 montserrat fw2 f3 charcoal'>{t('aboutIpfs.header')}</h2>
      <ul className='pl3'>
        <Trans i18nKey='aboutIpfs.paragraph1' t={t}>
          <li className='mb2'><strong>A decentralized data platform</strong> built on IPFS that gives you sovereign control over your data with HD wallet-based identity and end-to-end encryption</li>
        </Trans>
        <Trans i18nKey='aboutIpfs.paragraph2' t={t}>
          <li className='mb2'><strong>A peer-to-peer network</strong> where nodes discover each other, share data, and communicate without central servers or single points of failure</li>
        </Trans>
        <Trans i18nKey='aboutIpfs.paragraph3' t={t}>
          <li className='mb2'><strong>A cryptographic identity system</strong> &mdash; your HD wallet derives signing and encryption keys, giving you a portable identity that you own and control</li>
        </Trans>
        <Trans i18nKey='aboutIpfs.paragraph4' t={t}>
          <li className='mb2'><strong>Content-addressed storage</strong> &mdash; files are identified by their content hash, making them tamper-proof, cache-friendly, and globally distributable</li>
        </Trans>
        <Trans i18nKey='aboutIpfs.paragraph5' t={t}>
          <li className='mb2'><strong>An extensible plugin system</strong> for building decentralized applications and services on top of a robust peer-to-peer foundation</li>
        </Trans>
      </ul>
    </Box>
  )
}

export default withTranslation('welcome')(AboutIpfs)
