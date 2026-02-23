import React, { useState } from 'react'
import { connect } from 'redux-bundler-react'
import { withTranslation } from 'react-i18next'
import classnames from 'classnames'
import StrokeMarketing from '../icons/StrokeMarketing.js'
import StrokeWeb from '../icons/StrokeWeb.js'
import StrokeCube from '../icons/StrokeCube.js'
import StrokeSettings from '../icons/StrokeSettings.js'
import StrokeIpld from '../icons/StrokeIpld.js'
import StrokeLab from '../icons/StrokeLab.js'
import StrokeCode from '../icons/StrokeCode.js'
import StrokeDocument from '../icons/StrokeDocument.js'
import SdnLogo from '../icons/SdnLogo.js'
import TrustBadge from '../components/trust-badge/TrustBadge.js'
import TrustLevelsModal from '../components/trust-levels-modal/TrustLevelsModal.js'
import SignOutModal from '../components/sign-out-modal/SignOutModal.js'

// Styles
import './NavBar.css'

/**
 * @param {Object} props
 * @param {string} props.to
 * @param {React.ComponentType<React.SVGProps<SVGSVGElement>>} props.icon
 * @param {string} [props.alternative]
 * @param {boolean} [props.disabled]
 * @param {string} props.children
 */
const NavLink = ({
  to,
  icon,
  alternative,
  disabled,
  children
}) => {
  const Svg = icon
  const { hash } = window.location
  const href = `#${to}`
  const active = alternative
    ? hash === href || hash.startsWith(`${href}${alternative}`)
    : hash === href || hash.startsWith(`${href}/`)
  const anchorClass = classnames({
    'bg-white-10 navbar-item-active': active,
    'o-50 no-pointer-events': disabled
  }, ['navbar-item dib db-l pt1 pb2 pv1-l white no-underline f5 hover-bg-white-10 tc bb bw2 bw0-l b--navy'])
  const svgClass = classnames({
    'o-100': active,
    'o-70': !active
  }, ['fill-current-color'])

  return (
    // eslint-disable-next-line jsx-a11y/anchor-is-valid
    <a href={disabled ? undefined : href} onClick={(e) => e.currentTarget.blur()} className={anchorClass} role='menuitem' title={children}>
      <div className='db ph2 pv1'>
        <div className='db'>
          <Svg width='36' role='presentation' className={svgClass} />
        </div>
        <div className={`${active ? 'o-100' : 'o-70'} db f7 tc montserrat ttu fw1 navbar-item-label`}>
          {children}
        </div>
      </div>
    </a>
  )
}

/**
 * @param {Object} props
 * @param {import('i18next').TFunction} props.t
 */
export const NavBar = ({ t, isAdminUser: isAdmin, authUser, doLogout }) => {
  const [showSignOut, setShowSignOut] = useState(false)
  const [showTrustLevels, setShowTrustLevels] = useState(false)

  const codeUrl = 'https://github.com/ipfs/ipfs-webui'
  const bugsUrl = `${codeUrl}/issues`
  const gitRevision = process.env.REACT_APP_GIT_REV
  const revisionUrl = `${codeUrl}/commit/${gitRevision}`
  return (
    <div className='h-100 fixed-l flex flex-column justify-between' style={{ overflowY: 'hidden', width: 'inherit' }}>
      <div className='flex flex-column'>
        <a href="#/" role='menuitem' title='Space Data Network' className='no-underline'>
          <div className='pt2 pb1 pb1-l tc'>
            <div className='navbar-logo-vert center pt2 pb1' style={{ height: 70, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center' }}>
              <SdnLogo width={32} className='sdn-logo' />
              <span style={{ fontSize: '12px', fontWeight: 700, color: '#58a6ff', fontFamily: 'Montserrat, sans-serif', letterSpacing: '0.05em', marginTop: '2px' }}>SDN</span>
            </div>
          </div>
        </a>
        <div className='db overflow-x-scroll overflow-x-hidden-l nowrap tc' role='menubar'>
          <NavLink to='/' alternative="status" icon={StrokeMarketing}>{t('status:title')}</NavLink>
          <NavLink to='/files' icon={StrokeWeb}>{t('files:title')}</NavLink>
          <NavLink to='/explore' icon={StrokeIpld}>{t('explore:tabName')}</NavLink>
          <NavLink to='/schemas' icon={StrokeDocument}>Schemas</NavLink>
          <NavLink to='/plugins' icon={StrokeCode}>Plugins</NavLink>
          <NavLink to='/peers' icon={StrokeCube}>{t('peers:title')}</NavLink>
          <NavLink to='/settings' icon={StrokeSettings}>{t('settings:title')}</NavLink>
          <NavLink to='/diagnostics' icon={StrokeLab}>{t('diagnostics:title')}</NavLink>
        </div>
      </div>
      <div className='dn db-l navbar-footer tc center' style={{ padding: '12px 10px 14px' }}>
        {authUser && (
          <div style={{ marginBottom: 10 }}>
            <div style={{ color: 'rgba(255,255,255,0.85)', fontSize: 13, fontWeight: 600, marginBottom: 6 }}>
              {authUser.name || 'User'}
            </div>
            <TrustBadge
              level={authUser.trust_level}
              size='small'
              onClick={() => setShowTrustLevels(true)}
            />
          </div>
        )}
        <div style={{ marginBottom: 12 }}>
          <button
            className='pointer'
            onClick={() => setShowSignOut(true)}
            style={{
              padding: '7px 14px',
              borderRadius: 8,
              border: '1px solid rgba(88, 166, 255, 0.35)',
              background: 'transparent',
              color: 'white',
              fontWeight: 600,
              fontSize: 12
            }}
          >
            Sign out
          </button>
        </div>
        <div className='o-60' style={{ fontSize: 10, lineHeight: 1.6 }}>
          { gitRevision && <div>
            <a className='link white' href={revisionUrl} target='_blank' rel='noopener noreferrer'>{gitRevision}</a>
          </div> }
          <div>
            <a className='link white' href={codeUrl} target='_blank' rel='noopener noreferrer'>{t('app:nav.codeLink')}</a>
            {' · '}
            <a className='link white' href={bugsUrl} target='_blank' rel='noopener noreferrer'>{t('app:nav.bugsLink')}</a>
          </div>
        </div>
      </div>

      <SignOutModal
        show={showSignOut}
        onCancel={() => setShowSignOut(false)}
        onConfirm={() => { setShowSignOut(false); doLogout && doLogout() }}
      />
      <TrustLevelsModal
        show={showTrustLevels}
        onClose={() => setShowTrustLevels(false)}
      />
    </div>
  )
}

export default connect(
  'selectIsAdminUser',
  'selectAuthUser',
  'doLogout',
  withTranslation()(NavBar)
)
