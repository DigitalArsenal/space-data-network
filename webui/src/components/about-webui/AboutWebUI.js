import React from 'react'
import { withTranslation, Trans } from 'react-i18next'
import Box from '../box/Box.js'

/**
 * @param {Object} props
 * @param {import('i18next').TFunction} props.t
 */
export const AboutWebUI = ({ t }) => {
  return (
    <Box>
      <h2 className='mt0 mb3 montserrat fw2 f3 charcoal'>{t('welcomeInfo.header')}</h2>
      <ul className='pl3'>
        <Trans i18nKey='welcomeInfo.paragraph1' t={t}>
          <li className='mb2'><a href='#/' className='link blue u b'>Check your node status</a>, including how many peers you're connected to, your storage and bandwidth stats, and more</li>
        </Trans>
        <Trans i18nKey='welcomeInfo.paragraph2' t={t}>
          <li className='mb2'><a href='#/files' className='link blue u b'>View and manage files</a> in your IPFS repo, including drag-and-drop file import, easy pinning, and quick sharing and download options</li>
        </Trans>
        <Trans i18nKey='welcomeInfo.paragraph3' t={t}>
          <li className='mb2'><a href='#/explore' className='link blue b'>Explore content</a> with sample datasets and browse IPLD, the data model that underpins content-addressed storage</li>
        </Trans>
        <Trans i18nKey='welcomeInfo.paragraph4' t={t}>
          <li className='mb2'><a href='#/peers' className='link blue b'>Browse the peer directory</a> to see connected nodes, verify identities, and manage trust relationships</li>
        </Trans>
        <Trans i18nKey='welcomeInfo.paragraph5' t={t}>
          <li className='mb2'><a href='#/settings' className='link blue b'>Review or edit your node settings</a> &mdash; no command line required</li>
        </Trans>
      </ul>
    </Box>
  )
}

export default withTranslation('welcome')(AboutWebUI)
