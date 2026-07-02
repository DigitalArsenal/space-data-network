import React, { useEffect, useRef, useState } from 'react'
import * as flatbuffers from 'flatbuffers'
import QRCode from 'qrcode'
import { EPM } from 'spacedatastandards.org/lib/js/EPM/EPM.js'
import { KeyType } from 'spacedatastandards.org/lib/js/EPM/KeyType.js'
import Box from '../../../../../../webui/src/components/box/Box.js'
import Button from '../../../../../../webui/src/components/button/button.tsx'
import CameraIcon from '../../../../../../webui/src/icons/StrokeCamera.tsx'
import PictureIcon from '../../../../../../webui/src/icons/StrokePicture.tsx'
import TrashIcon from '../../../../../../webui/src/icons/StrokeTrash.tsx'
import Title from '../../../../../../webui/src/settings/Title.js'
import UpstreamSettingsPage from '../../../../../../webui/src/settings/LoadableSettingsPage.js'

const textFields = [
  ['dn', 'Display name'],
  ['legal_name', 'Legal name'],
  ['given_name', 'First name'],
  ['family_name', 'Last Name'],
  ['additional_name', 'Additional name'],
  ['honorific_prefix', 'Honorific prefix'],
  ['honorific_suffix', 'Honorific suffix'],
  ['job_title', 'Job title'],
  ['occupation', 'Occupation'],
  ['email', 'Email'],
  ['telephone', 'Telephone'],
]

const addressFields = [
  ['street', 'Street'],
  ['locality', 'City'],
  ['region', 'Region'],
  ['postal_code', 'Postal code'],
  ['country', 'Country'],
  ['po_box', 'P.O. box'],
]

const profileQRIdentityDomains = {
  signing: 'signing.spacedatanetwork.org',
  encryption: 'encryption.spacedatanetwork.org',
  bitcoin: 'bitcoin.spacedatanetwork.org',
  ethereum: 'ethereum.spacedatanetwork.org',
  solana: 'solana.spacedatanetwork.org',
}

const settingsTabs = [
  ['profile', 'Profile'],
  ['server-admin', 'Server Admin'],
  ['node-settings', 'Node Settings'],
]

const serverAdminPermissionLevels = [
  ['limited', 'Limited'],
  ['standard', 'Standard'],
  ['trusted', 'Trusted'],
  ['admin', 'Admin'],
]

const profileGridStyle = {
  display: 'grid',
  gridTemplateColumns: 'repeat(auto-fit, minmax(16rem, 1fr))',
  columnGap: '1rem',
}

const profileActionButtonStyle = {
  alignItems: 'center',
  display: 'inline-flex',
  justifyContent: 'center',
  textAlign: 'center',
}

const profilePhotoActionsClassName = 'mt2 mb3'

const profilePhotoActionsStyle = {
  display: 'grid',
  gap: '0.5rem',
  gridTemplateColumns: 'repeat(3, minmax(0, 1fr))',
}

const profilePhotoButtonStyle = {
  alignItems: 'center',
  display: 'inline-flex',
  flexDirection: 'column',
  gap: '0.15rem',
  justifyContent: 'center',
  minHeight: '3rem',
  minWidth: 0,
  textAlign: 'center',
  whiteSpace: 'nowrap',
  width: '100%',
}

const profileShareButtonStyle = {
  ...profileActionButtonStyle,
  color: '#ffffff',
  minWidth: 180,
  width: '100%',
}

const profileImageModalOverlayStyle = {
  background: 'rgba(7, 58, 70, 0.48)',
  backdropFilter: 'blur(4px)',
  zIndex: 9999,
}

const profileImageModalCardStyle = {
  maxWidth: 'min(92vw, 48rem)',
}

const profileImageModalImageStyle = {
  maxHeight: '72vh',
  objectFit: 'contain',
}

const settingsTabButtonStyle = {
  alignItems: 'center',
  borderBottomStyle: 'solid',
  borderBottomWidth: 3,
  display: 'inline-flex',
  justifyContent: 'center',
  minHeight: '3rem',
  textAlign: 'center',
}

const serverAdminTableStyle = {
  minWidth: '56rem',
}

function SettingsPage() {
  const [activeTab, setActiveTab] = useState('profile')

  return (
    <div className='mw9 center ph3 ph4-l'>
      <div className='mb3 bg-white ba b--black-10 br2 overflow-hidden'>
        <div className='flex overflow-auto'>
          {settingsTabs.map(([key, label]) => {
            const isActive = activeTab === key
            return (
              <button
                key={key}
                className={`button-reset pointer ph3 ph4-l fw6 nowrap ${isActive ? 'teal bg-snow-muted' : 'blue bg-white hover-bg-near-white'}`}
                style={{
                  ...settingsTabButtonStyle,
                  borderBottomColor: isActive ? '#69c4cf' : 'transparent',
                }}
                type='button'
                aria-selected={isActive}
                onClick={() => setActiveTab(key)}
              >
                {label}
              </button>
            )
          })}
        </div>
      </div>

      {activeTab === 'profile' && <NodeProfileSection />}
      {activeTab === 'server-admin' && <ServerAdminSection />}
      {activeTab === 'node-settings' && <UpstreamSettingsPage />}
    </div>
  )
}

function NodeProfileSection() {
  const fileRef = useRef(null)
  const videoRef = useRef(null)
  const streamRef = useRef(null)
  const [profile, setProfile] = useState(emptyProfile)
  const [epmPayload, setEPMPayload] = useState({})
  const [status, setStatus] = useState('loading')
  const [error, setError] = useState(null)
  const [cameraOpen, setCameraOpen] = useState(false)
  const [qrURL, setQRURL] = useState('')
  const [qrError, setQRError] = useState(null)
  const [imageModalOpen, setImageModalOpen] = useState(false)

  useEffect(() => {
    let cancelled = false
    async function loadProfile() {
      setStatus('loading')
      setError(null)
      try {
        const response = await fetch('/api/node/epm/json', { credentials: 'include' })
        if (!response.ok) {
          throw new Error(`profile request failed (${response.status})`)
        }
        const payload = await response.json()
        if (!cancelled) {
          setProfile(profileFromPayload(payload))
          setEPMPayload(payload ?? {})
          setStatus('ready')
        }
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : String(err))
          setStatus('error')
        }
      }
    }
    loadProfile()
    return () => {
      cancelled = true
      stopCamera(streamRef.current)
    }
  }, [])

  useEffect(() => {
    let cancelled = false

    async function renderQR() {
      setQRError(null)
      try {
        const dataURL = await QRCode.toDataURL(nodeProfileQRVCard(epmPayload, profile), {
          errorCorrectionLevel: 'L',
          margin: 4,
          width: 512,
          color: {
            dark: '#000000',
            light: '#ffffff',
          },
        })
        if (!cancelled) {
          setQRURL(dataURL)
        }
      } catch (err) {
        if (!cancelled) {
          setQRURL('')
          setQRError(err instanceof Error ? err.message : String(err))
        }
      }
    }

    renderQR()

    return () => {
      cancelled = true
    }
  }, [epmPayload, profile])

  async function saveProfile() {
    setStatus('saving')
    setError(null)
    try {
      const response = await fetch('/api/node/epm', {
        method: 'PUT',
        credentials: 'include',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify(cleanProfile(profile)),
      })
      if (!response.ok) {
        throw new Error(await response.text())
      }
      const payload = await response.json()
      setProfile(profileFromPayload(payload))
      setEPMPayload(payload ?? {})
      setStatus('saved')
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setStatus('error')
    }
  }

  async function handlePhotoFile(event) {
    const input = event.currentTarget
    const file = input.files?.[0]
    if (!file) {
      return
    }
    try {
      const dataUrl = await readFileAsDataURL(file)
      setProfile((current) => ({ ...current, photo_data_url: dataUrl }))
    } finally {
      input.value = ''
    }
  }

  async function openCamera() {
    setError(null)
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ video: true })
      streamRef.current = stream
      setCameraOpen(true)
      if (videoRef.current) {
        videoRef.current.srcObject = stream
        await videoRef.current.play()
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  function closeCamera() {
    stopCamera(streamRef.current)
    streamRef.current = null
    setCameraOpen(false)
  }

  function capturePhoto() {
    const video = videoRef.current
    if (!video) {
      return
    }
    const canvas = document.createElement('canvas')
    canvas.width = video.videoWidth || 640
    canvas.height = video.videoHeight || 480
    const context = canvas.getContext('2d')
    if (!context) {
      setError('Camera capture is not available in this browser.')
      return
    }
    context.drawImage(video, 0, 0, canvas.width, canvas.height)
    setProfile((current) => ({ ...current, photo_data_url: canvas.toDataURL('image/png') }))
    closeCamera()
  }

  function removePhoto() {
    if (!profile.photo_data_url) {
      return
    }
    if (!window.confirm('Are you sure you want to remove this profile image?')) {
      return
    }
    setImageModalOpen(false)
    setProfile((current) => ({ ...current, photo_data_url: '' }))
  }

  return (
    <Box className='mb3 pa4-l pa3'>
      <div className='flex flex-column flex-row-l justify-between-l'>
        <div className='pr4-l'>
          <Title>Profile</Title>
          <p className='ma0 mb3 lh-copy charcoal f6'>
            Edit this node&apos;s SDN profile. The daemon stores the EPM in the local FlatSQL directory and exposes it as vCard and QR code.
          </p>
        </div>
        <div className='tc tl-l mt3 mt0-l'>
          {profile.photo_data_url
            ? (
              <button className='button-reset pointer br-100 focus-outline' type='button' aria-label='Open profile image preview' onClick={() => setImageModalOpen(true)}>
                <img className='br-100 ba b--black-10 bg-snow-muted object-cover db' alt='Node profile' src={profile.photo_data_url} style={{ width: 96, height: 96 }} />
              </button>
              )
            : <div className='br-100 ba b--black-10 bg-snow-muted flex items-center justify-center teal f2' style={{ width: 96, height: 96 }}>S</div>}
        </div>
      </div>

      <div className='mt3 flex flex-column flex-row-l'>
        <div className='w-100 w-two-thirds-l pr0 pr4-l'>
          <div style={profileGridStyle}>
            {textFields.map(([key, label]) => (
              <label className='db mb3' key={key}>
                <span className='db f6 ttu tracked black-60 mb1'>{label}</span>
                <input
                  className='input-reset ba b--black-20 pa2 br2 w-100'
                  value={profile[key] ?? ''}
                  onChange={(event) => {
                    const { value } = event.target
                    setProfile((current) => ({ ...current, [key]: value }))
                  }}
                />
              </label>
            ))}
          </div>

          <h3 className='f5 mt2 mb2 charcoal'>Address</h3>
          <div style={profileGridStyle}>
            {addressFields.map(([key, label]) => (
              <label className='db mb3' key={key}>
                <span className='db f6 ttu tracked black-60 mb1'>{label}</span>
                <input
                  className='input-reset ba b--black-20 pa2 br2 w-100'
                  value={profile.address?.[key] ?? ''}
                  onChange={(event) => {
                    const { value } = event.target
                    setProfile((current) => ({
                      ...current,
                      address: {
                        ...(current.address ?? {}),
                        [key]: value,
                      },
                    }))
                  }}
                />
              </label>
            ))}
          </div>
        </div>

        <aside className='w-100 w-third-l pl0 pl4-l bl-l b--black-10'>
          <h3 className='f5 mt0 mb2 charcoal'>Photo</h3>
          <input ref={fileRef} className='dn' type='file' accept='image/*' onChange={handlePhotoFile} />
          <div className={profilePhotoActionsClassName} style={profilePhotoActionsStyle}>
            <Button minWidth={0} className='ma0' style={profilePhotoButtonStyle} buttonType='button' onClick={() => fileRef.current?.click()}>
              <ProfileActionIcon Icon={PictureIcon} />
              <ProfileActionText>Upload</ProfileActionText>
            </Button>
            <Button minWidth={0} bg='bg-white' color='blue' fill='fill-blue' className='ma0 ba b--black-20' style={profilePhotoButtonStyle} buttonType='button' onClick={openCamera}>
              <ProfileActionIcon Icon={CameraIcon} />
              <ProfileActionText>Take</ProfileActionText>
            </Button>
            <Button minWidth={0} danger className='ma0' style={profilePhotoButtonStyle} buttonType='button' onClick={removePhoto}>
              <ProfileActionIcon Icon={TrashIcon} />
              <ProfileActionText>Remove</ProfileActionText>
            </Button>
          </div>

          {cameraOpen && (
            <div className='mt3 pa2 ba b--black-10 br2 bg-snow-muted'>
              <video ref={videoRef} className='db w-100 bg-charcoal' playsInline muted />
              <div className='mt2 flex'>
                <Button minWidth={120} className='mr2' style={profileActionButtonStyle} buttonType='button' onClick={capturePhoto}>Capture</Button>
                <Button minWidth={100} bg='bg-white' color='blue' fill='fill-blue' className='ba b--black-20' style={profileActionButtonStyle} buttonType='button' onClick={closeCamera}>Cancel</Button>
              </div>
            </div>
          )}

          <h3 className='f5 mt4 mb2 charcoal'>Share</h3>
          <div className='flex flex-column'>
            <div className='pa2 ba b--black-10 br2 bg-white tc'>
              {qrURL ? (
                <img alt='Node profile QR code' src={qrURL} style={{ maxWidth: '100%', width: 180 }} />
              ) : (
                <div className='f6 black-60 pv3'>
                  {qrError ? `QR unavailable: ${qrError}` : 'Loading QR code...'}
                </div>
              )}
            </div>
            <a className='Button transition-all sans-serif inline-flex items-center justify-center v-mid fw5 nowrap lh-copy bn br1 pa2 focus-outline bg-teal white fill-white tc link mt2' style={profileShareButtonStyle} href='/api/node/epm/vcard' download='sdn-node.vcf'>
              Download .vcf
            </a>
          </div>
        </aside>
      </div>

      <div className='mt3 flex flex-column flex-row-ns items-start items-center-ns justify-between-ns'>
        <div className='f6 black-60 mb3 mb0-ns'>
          {status === 'loading' ? 'Loading node profile...' : status === 'saving' ? 'Saving profile...' : status === 'saved' ? 'Profile saved.' : 'Profile is loaded from the node EPM.'}
          {error && <span className='dark-red ml0 ml2-ns db dib-ns'>{error}</span>}
        </div>
        <Button minWidth={140} disabled={status === 'saving'} style={profileActionButtonStyle} onClick={saveProfile} buttonType='button'>
          Save profile
        </Button>
      </div>

      {imageModalOpen && profile.photo_data_url && (
        <ProfileImageModal imageURL={profile.photo_data_url} onClose={() => setImageModalOpen(false)} />
      )}
    </Box>
  )
}

function ServerAdminSection() {
  const fileRef = useRef(null)
  const [users, setUsers] = useState([])
  const [status, setStatus] = useState('loading')
  const [error, setError] = useState(null)
  const [permissionLevel, setPermissionLevel] = useState('standard')
  const [grantStatus, setGrantStatus] = useState('idle')
  const [grantError, setGrantError] = useState(null)

  useEffect(() => {
    let cancelled = false

    async function load() {
      setStatus('loading')
      setError(null)
      try {
        const loadedUsers = await loadServerAdminUsers()
        if (!cancelled) {
          setUsers(loadedUsers)
          setStatus('ready')
        }
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : String(err))
          setStatus('error')
        }
      }
    }

    load()

    return () => {
      cancelled = true
    }
  }, [])

  async function reloadUsers() {
    const loadedUsers = await loadServerAdminUsers()
    setUsers(loadedUsers)
    setStatus('ready')
  }

  async function handleGrantFile(event) {
    const input = event.currentTarget
    const file = input.files?.[0]
    if (!file) {
      return
    }

    setGrantStatus('loading')
    setGrantError(null)

    try {
      const [bytes, text] = await Promise.all([
        file.arrayBuffer().then((buffer) => new Uint8Array(buffer)),
        file.text(),
      ])
      const grant = serverAdminGrantFromFilePayload(file.name, bytes, text, permissionLevel)
      await saveServerAdminGrant(grant)
      await reloadUsers()
      setGrantStatus(`Granted ${serverAdminPermissionLabel(grant.trust_level)} backend access to ${grant.name || grant.xpub}.`)
    } catch (err) {
      setGrantError(err instanceof Error ? err.message : String(err))
      setGrantStatus('error')
    } finally {
      input.value = ''
    }
  }

  return (
    <Box className='mb3 pa4-l pa3'>
      <div className='flex flex-column flex-row-l justify-between-l items-start-l mb3'>
        <div className='pr4-l'>
          <Title>Server Admin</Title>
          <p className='ma0 lh-copy charcoal f6'>
            Grant backend access by importing trusted SDN vCards or EPM records. Permissions map to the server auth trust levels used by protected APIs.
          </p>
        </div>
        <div className='mt3 mt0-l f6 ttu tracked black-60'>
          {status === 'loading' ? 'Loading access list...' : `${users.length} grants`}
        </div>
      </div>

      <section className='pa3 ba b--black-10 br2 bg-white mb3'>
        <div className='flex flex-column flex-row-l'>
          <div className='flex-auto pr0 pr4-l mb3 mb0-l'>
            <label className='db mb3' htmlFor='server-admin-permission-level'>
              <span className='db f6 ttu tracked black-60 mb1'>Server backend permission</span>
              <select
                id='server-admin-permission-level'
                name='server-admin-permission-level'
                className='input-reset ba b--black-20 pa2 br2 bg-white w-100'
                value={permissionLevel}
                onChange={(event) => {
                  const { value } = event.currentTarget
                  setPermissionLevel(value)
                }}
              >
                {serverAdminPermissionLevels.map(([value, label]) => (
                  <option key={value} value={value}>{label}</option>
                ))}
              </select>
            </label>
            <p className='mt0 mb0 f6 lh-copy black-60'>
              Upload reads the xpub and signing public key from the vCard/EPM, then grants the selected permission level through the server auth API.
            </p>
          </div>

          <div className='w-100 w-third-l pl0 pl4-l bl-l b--black-10'>
            <h3 className='f4 mt0 mb2'>Grant access</h3>
            <input
              ref={fileRef}
              className='dn'
              type='file'
              accept='.vcf,.vcard,.json,application/json,text/vcard,text/x-vcard'
              onChange={handleGrantFile}
            />
            <Button minWidth={170} style={profileActionButtonStyle} onClick={() => fileRef.current?.click()} buttonType='button'>
              Upload vCard / EPM
            </Button>
            {grantStatus !== 'idle' && grantStatus !== 'error' && (
              <div className='mt2 f6 black-60'>{grantStatus === 'loading' ? 'Granting backend access...' : grantStatus}</div>
            )}
            {grantError && <div className='mt2 dark-red f6 lh-copy'>{grantError}</div>}
          </div>
        </div>
      </section>

      <section className='ba b--black-10 br2 bg-white'>
        <div className='pa3 bb b--black-10'>
          <h3 className='f4 mt0 mb1'>Backend access grants</h3>
          <p className='ma0 f6 black-60'>
            Authenticated wallets with these xpubs can access the server backend at or above their permission level.
          </p>
        </div>
        {error && <div className='pa3 dark-red f6 lh-copy'>{error}</div>}
        {!error && users.length > 0 ? (
          <div className='overflow-auto'>
            <table className='collapse w-100 f6' style={serverAdminTableStyle}>
              <thead>
                <tr className='tl bg-snow-muted'>
                  <th className='bb b--black-10 pa3'>Name</th>
                  <th className='bb b--black-10 pa3'>Permission</th>
                  <th className='bb b--black-10 pa3'>Xpub</th>
                  <th className='bb b--black-10 pa3'>Signing key</th>
                </tr>
              </thead>
              <tbody>
                {users.map((user) => (
                  <tr key={user.xpub}>
                    <td className='bt b--black-10 pa3 fw6 charcoal'>{user.name || '—'}</td>
                    <td className='bt b--black-10 pa3'>
                      <span className='dib br-pill bg-aqua white ttu tracked f7 ph2 pv1'>{serverAdminPermissionLabel(user.trust_level)}</span>
                    </td>
                    <td className='bt b--black-10 pa3 monospace truncate' title={user.xpub}>{user.xpub}</td>
                    <td className='bt b--black-10 pa3 monospace truncate' title={user.signing_pubkey_hex}>{user.signing_pubkey_hex || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : !error && (
          <div className='pa3 black-60'>
            {status === 'loading' ? 'Loading backend access grants...' : 'No backend access grants are configured.'}
          </div>
        )}
      </section>
    </Box>
  )
}

function ProfileActionIcon({ Icon }) {
  return (
    <span className='flex items-center justify-center' aria-hidden='true'>
      <Icon className='fill-current-color' width={16} height={16} />
    </span>
  )
}

function ProfileActionText({ children }) {
  return <span className='db tc truncate f7'>{children}</span>
}

function ProfileImageModal({ imageURL, onClose }) {
  useEffect(() => {
    function handleKeyDown(event) {
      if (event.key === 'Escape') {
        onClose()
      }
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [onClose])

  return (
    <div className='fixed absolute--fill flex items-center justify-center pa3' style={profileImageModalOverlayStyle} role='dialog' aria-modal='true' aria-label='Profile image preview'>
      <button className='absolute absolute--fill button-reset pointer' type='button' aria-label='Close profile image preview' onClick={onClose} />
      <div className='relative bg-white br3 shadow-5 pa3 pa4-ns' style={profileImageModalCardStyle}>
        <div className='flex justify-between items-center mb3'>
          <h2 className='f4 ma0 charcoal'>Profile image</h2>
          <button className='button-reset pointer teal hover-aqua fw6' type='button' onClick={onClose}>
            Close
          </button>
        </div>
        <img className='db w-100 br2 bg-snow-muted' alt='Node profile preview' src={imageURL} style={profileImageModalImageStyle} />
      </div>
    </div>
  )
}

function nodeProfileQRVCard(epmPayload, profile) {
  const address = profile.address ?? epmPayload.address ?? {}
  const displayName = firstProfileString(profile.dn, epmPayload.dn, 'Space Data Network Node')
  const familyName = firstProfileString(profile.family_name, epmPayload.family_name)
  let givenName = firstProfileString(profile.given_name, epmPayload.given_name)
  const additionalName = firstProfileString(profile.additional_name, epmPayload.additional_name)
  const honorificPrefix = firstProfileString(profile.honorific_prefix, epmPayload.honorific_prefix)
  const honorificSuffix = firstProfileString(profile.honorific_suffix, epmPayload.honorific_suffix)
  if (!familyName && !givenName && !additionalName && !honorificPrefix && !honorificSuffix) {
    givenName = displayName
  }
  const lines = [
    'BEGIN:VCARD',
    'VERSION:3.0',
    'PRODID;VALUE=TEXT:-//Apple Inc.//iPhone OS 15.1.1//EN',
    'N:' + [
      familyName,
      givenName,
      additionalName,
      honorificPrefix,
      honorificSuffix,
    ].map(escapeVCardValue).join(';'),
    'FN:' + escapeVCardValue(displayName),
  ]

  addProfileVCardLine(lines, 'ORG', firstProfileString(profile.legal_name, epmPayload.legal_name))
  addProfileVCardLine(lines, 'EMAIL', firstProfileString(profile.email, epmPayload.email))
  addProfileVCardLine(lines, 'TEL', firstProfileString(profile.telephone, epmPayload.telephone))
  addProfileVCardLine(lines, 'TITLE', firstProfileString(profile.job_title, epmPayload.job_title))
  addProfileVCardLine(lines, 'ROLE', firstProfileString(profile.occupation, epmPayload.occupation))
  addProfileAddressLine(lines, address)
  addProfileVCardLine(lines, 'UID', firstProfileString(epmPayload.peer_id))
  addProfileVCardLine(lines, 'X-SDN-DIRECTORY-KIND', firstProfileString(epmPayload.directory_kind, epmPayload.entity_type, 'node'))
  addProfileVCardLine(lines, 'X-SDN-PEER-ID', firstProfileString(epmPayload.peer_id))
  addProfileVCardLine(lines, 'X-SDN-EPM-CID', firstProfileString(epmPayload.epm_cid))
  addProfileQRIdentityEmailLines(lines, epmPayload)

  lines.push('END:VCARD')
  return lines.map(foldVCardLine).join('\r\n') + '\r\n'
}

function addProfileVCardLine(lines, key, value) {
  const trimmed = firstProfileString(value)
  if (!trimmed) {
    return
  }
  lines.push(`${key}:${escapeVCardValue(trimmed)}`)
}

function addProfileAddressLine(lines, address) {
  if (!address || typeof address !== 'object') {
    return
  }
  const parts = [
    firstProfileString(address.po_box),
    '',
    firstProfileString(address.street),
    firstProfileString(address.locality),
    firstProfileString(address.region),
    firstProfileString(address.postal_code),
    firstProfileString(address.country),
  ]
  if (!parts.some(Boolean)) {
    return
  }
  lines.push('ADR;TYPE=WORK:' + parts.map(escapeVCardValue).join(';'))
}

function addProfileQRIdentityEmailLines(lines, epmPayload) {
  const seen = new Set()
  const addAlias = (type, value) => {
    const trimmed = firstProfileString(value)
    const domain = profileQRIdentityDomains[type]
    if (!trimmed || !domain || !isSafeEmailLocalPart(trimmed)) {
      return
    }
    const line = `EMAIL;type=INTERNET;type=${type}:${trimmed}@${domain}`
    if (seen.has(line)) {
      return
    }
    seen.add(line)
    lines.push(line)
  }

  addAlias('signing', firstProfileString(epmPayload.signing_pubkey_hex, findProfileKey(epmPayload, 'signing')))
  addAlias('encryption', firstProfileString(epmPayload.encryption_pubkey_hex, findProfileKey(epmPayload, 'encryption')))
  addAlias('bitcoin', firstProfileString(epmPayload.bitcoin_address, findProfileChainAddress(epmPayload, 'bitcoin')))
  addAlias('ethereum', firstProfileString(epmPayload.ethereum_address, findProfileChainAddress(epmPayload, 'ethereum')))
  addAlias('solana', firstProfileString(epmPayload.solana_address, findProfileChainAddress(epmPayload, 'solana')))
}

function findProfileKey(epmPayload, type) {
  const keys = Array.isArray(epmPayload.keys) ? epmPayload.keys : []
  for (const key of keys) {
    const publicKey = firstProfileString(key?.public_key, key?.PUBLIC_KEY)
    if (!publicKey) {
      continue
    }
    const keyType = firstProfileString(key?.key_type, key?.KEY_TYPE).toLowerCase()
    const addressType = firstProfileString(key?.address_type, key?.ADDRESS_TYPE).toLowerCase()
    if (type === 'encryption' && (keyType === 'encryption' || addressType === 'x25519')) {
      return publicKey
    }
    if (type === 'signing' && (keyType === 'signing' || (addressType && addressType !== 'x25519'))) {
      return publicKey
    }
  }
  return ''
}

function findProfileChainAddress(epmPayload, chain) {
  const proofs = Array.isArray(epmPayload.chain_proofs) ? epmPayload.chain_proofs : []
  for (const proof of proofs) {
    if (firstProfileString(proof?.chain, proof?.CHAIN).toLowerCase() === chain) {
      return firstProfileString(proof?.address, proof?.ADDRESS)
    }
  }
  return ''
}

function firstProfileString(...values) {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) {
      return value.trim()
    }
    if (typeof value === 'number' && Number.isFinite(value)) {
      return String(value)
    }
  }
  return ''
}

function isSafeEmailLocalPart(value) {
  return /^[A-Za-z0-9._+-]+$/.test(String(value ?? '').trim())
}

function foldVCardLine(line) {
  const value = String(line)
  if (value.length <= 74) {
    return value
  }
  const chunks = []
  for (let offset = 0; offset < value.length; offset += 74) {
    chunks.push(value.slice(offset, offset + 74))
  }
  return chunks.join('\r\n ')
}

function escapeVCardValue(value) {
  return String(value ?? '')
    .replace(/\\/g, '\\\\')
    .replace(/\n/g, '\\n')
    .replace(/,/g, '\\,')
    .replace(/;/g, '\\;')
}

function emptyProfile() {
  return {
    address: {},
  }
}

function profileFromPayload(payload) {
  return {
    ...emptyProfile(),
    ...pickProfileFields(payload ?? {}),
    address: {
      ...(payload?.address ?? {}),
    },
  }
}

function pickProfileFields(payload) {
  const profile = {}
  for (const [key] of textFields) {
    if (typeof payload[key] === 'string') {
      profile[key] = payload[key]
    }
  }
  if (typeof payload.photo_data_url === 'string') {
    profile.photo_data_url = payload.photo_data_url
  }
  return profile
}

function cleanProfile(profile) {
  const cleaned = {}
  for (const [key] of textFields) {
    if (String(profile[key] ?? '').trim()) {
      cleaned[key] = String(profile[key]).trim()
    }
  }
  if (String(profile.photo_data_url ?? '').trim()) {
    cleaned.photo_data_url = String(profile.photo_data_url).trim()
  }
  const address = {}
  for (const [key] of addressFields) {
    if (String(profile.address?.[key] ?? '').trim()) {
      address[key] = String(profile.address[key]).trim()
    }
  }
  if (Object.keys(address).length > 0) {
    cleaned.address = address
  }
  return cleaned
}

function readFileAsDataURL(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result ?? ''))
    reader.onerror = () => reject(reader.error ?? new Error('failed to read file'))
    reader.readAsDataURL(file)
  })
}

function stopCamera(stream) {
  if (!stream) {
    return
  }
  for (const track of stream.getTracks()) {
    track.stop()
  }
}

async function loadServerAdminUsers() {
  const response = await fetch('/api/auth/users', { credentials: 'include' })
  if (!response.ok) {
    throw new Error(await response.text())
  }
  const payload = await response.json()
  return Array.isArray(payload) ? payload : []
}

async function saveServerAdminGrant(grant) {
  const body = JSON.stringify(grant)
  const response = await fetch('/api/auth/users', {
    method: 'POST',
    credentials: 'include',
    headers: {
      'Content-Type': 'application/json',
    },
    body,
  })
  if (response.status === 409) {
    const updateResponse = await fetch(`/api/auth/users/${encodeURIComponent(grant.xpub)}`, {
      method: 'PUT',
      credentials: 'include',
      headers: {
        'Content-Type': 'application/json',
      },
      body,
    })
    if (!updateResponse.ok) {
      throw new Error(await updateResponse.text())
    }
    return
  }
  if (!response.ok) {
    throw new Error(await response.text())
  }
}

function serverAdminGrantFromFilePayload(filename, bytes, text, permissionLevel) {
  try {
    return serverAdminGrantFromText(text, permissionLevel)
  } catch (textErr) {
    try {
      return serverAdminGrantFromEPMBytes(bytes, permissionLevel)
    } catch (binaryErr) {
      const suffix = filename ? ` (${filename})` : ''
      throw new Error(`Unable to read backend access identity from uploaded vCard or EPM${suffix}: ${textErr.message || textErr}; ${binaryErr.message || binaryErr}`)
    }
  }
}

function serverAdminGrantFromText(text, permissionLevel) {
  const trimmed = String(text ?? '').trim()
  if (!trimmed) {
    throw new Error('uploaded vCard/EPM is empty')
  }
  if (/^BEGIN:VCARD/i.test(trimmed)) {
    return serverAdminGrantFromVCard(trimmed, permissionLevel)
  }
  return serverAdminGrantFromJSON(JSON.parse(trimmed), permissionLevel)
}

function serverAdminGrantFromJSON(payload, permissionLevel) {
  const xpub = extractServerAdminXPubFromJSON(payload)
  if (!xpub) {
    throw new Error('uploaded EPM JSON does not include an xpub')
  }
  return {
    xpub,
    name: firstProfileString(extractServerAdminNameFromJSON(payload), 'Imported SDN identity'),
    trust_level: permissionLevel,
    signing_pubkey_hex: extractServerAdminSigningKeyFromJSON(payload),
  }
}

function serverAdminGrantFromVCard(vcard, permissionLevel) {
  const xpub = extractServerAdminXPubFromVCard(vcard)
  if (!xpub) {
    throw new Error('uploaded vCard does not include an xpub')
  }
  return {
    xpub,
    name: firstProfileString(extractServerAdminNameFromVCard(vcard), 'Imported SDN identity'),
    trust_level: permissionLevel,
    signing_pubkey_hex: extractServerAdminSigningKeyFromVCard(vcard),
  }
}

function serverAdminGrantFromEPMBytes(bytes, permissionLevel) {
  if (!(bytes instanceof Uint8Array) || bytes.length === 0) {
    throw new Error('uploaded EPM binary is empty')
  }

  const epm = readServerAdminEPM(bytes)
  const keys = serverAdminEPMKeys(epm)
  const signingKey = keys.find((key) => key.keyType === KeyType.Signing)
  const xpub = firstProfileString(signingKey?.xpub, ...keys.map((key) => key.xpub))
  if (!xpub) {
    throw new Error('uploaded binary EPM does not include an xpub')
  }

  return {
    xpub,
    name: firstProfileString(epm.DN(), epm.LEGAL_NAME(), 'Imported SDN identity'),
    trust_level: permissionLevel,
    signing_pubkey_hex: firstProfileString(signingKey?.publicKey),
  }
}

function readServerAdminEPM(bytes) {
  const sizePrefixed = new flatbuffers.ByteBuffer(bytes)
  try {
    const epm = EPM.getSizePrefixedRootAsEPM(sizePrefixed)
    if (epm && (epm.DN() || epm.LEGAL_NAME() || epm.keysLength() > 0)) {
      return epm
    }
  } catch {
    // Fall back to non-size-prefixed buffers below.
  }
  const plain = new flatbuffers.ByteBuffer(bytes)
  return EPM.getRootAsEPM(plain)
}

function serverAdminEPMKeys(epm) {
  const keys = []
  for (let index = 0; index < epm.keysLength(); index += 1) {
    const key = epm.KEYS(index)
    if (!key) {
      continue
    }
    keys.push({
      keyType: key.KEY_TYPE(),
      publicKey: key.PUBLIC_KEY(),
      xpub: key.XPUB(),
    })
  }
  return keys
}

function extractServerAdminXPubFromJSON(payload) {
  const objects = collectServerAdminJSONObjects(payload)
  for (const object of objects) {
    const direct = firstProfileString(object.xpub, object.XPUB)
    if (direct) {
      return direct
    }
    const keys = Array.isArray(object.keys) ? object.keys : Array.isArray(object.KEYS) ? object.KEYS : []
    for (const key of keys) {
      const value = firstProfileString(key?.xpub, key?.XPUB)
      if (value) {
        return value
      }
    }
  }
  return ''
}

function extractServerAdminSigningKeyFromJSON(payload) {
  const objects = collectServerAdminJSONObjects(payload)
  for (const object of objects) {
    const direct = firstProfileString(object.signing_pubkey_hex, object.signing_public_key, object.SIGNING_PUBKEY_HEX)
    if (direct) {
      return direct
    }
    const keys = Array.isArray(object.keys) ? object.keys : Array.isArray(object.KEYS) ? object.KEYS : []
    for (const key of keys) {
      const keyType = firstProfileString(key?.key_type, key?.KEY_TYPE).toLowerCase()
      if (keyType === 'signing') {
        const publicKey = firstProfileString(key?.public_key, key?.PUBLIC_KEY)
        if (publicKey) {
          return publicKey
        }
      }
    }
  }
  return ''
}

function extractServerAdminNameFromJSON(payload) {
  const objects = collectServerAdminJSONObjects(payload)
  for (const object of objects) {
    const name = firstProfileString(object.dn, object.DN, object.name, object.legal_name, object.LEGAL_NAME)
    if (name) {
      return name
    }
  }
  return ''
}

function collectServerAdminJSONObjects(value, collected = [], depth = 0) {
  if (depth > 4 || value == null) {
    return collected
  }
  if (typeof value === 'string') {
    const trimmed = value.trim()
    if (trimmed.startsWith('{') || trimmed.startsWith('[')) {
      try {
        collectServerAdminJSONObjects(JSON.parse(trimmed), collected, depth + 1)
      } catch {
        // Ignore non-JSON strings.
      }
    }
    return collected
  }
  if (Array.isArray(value)) {
    for (const item of value) {
      collectServerAdminJSONObjects(item, collected, depth + 1)
    }
    return collected
  }
  if (typeof value === 'object') {
    collected.push(value)
    for (const key of ['epm_json', 'EPM_JSON', 'epm', 'EPM', 'record', 'RECORD']) {
      if (value[key] !== undefined) {
        collectServerAdminJSONObjects(value[key], collected, depth + 1)
      }
    }
  }
  return collected
}

function extractServerAdminXPubFromVCard(vcard) {
  const lines = serverAdminVCardLines(vcard)
  const direct = firstProfileString(
    serverAdminVCardValue(lines, 'X-SDN-XPUB'),
    serverAdminVCardValue(lines, 'X-XPUB'),
    serverAdminVCardValue(lines, 'XPUB'),
    ...serverAdminVCardRelatedValues(lines, 'Extended Public Key'),
  )
  if (direct) {
    return direct
  }
  const match = serverAdminUnfoldVCard(vcard).match(/\bxpub[A-Za-z0-9]+\b/)
  return match ? match[0] : ''
}

function extractServerAdminSigningKeyFromVCard(vcard) {
  const lines = serverAdminVCardLines(vcard)
  return firstProfileString(
    serverAdminVCardValue(lines, 'X-SIGNING-KEY'),
    serverAdminVCardEmailAlias(lines, 'signing.spacedatanetwork.org'),
    ...serverAdminVCardRelatedValues(lines, 'Public Key Signing'),
  )
}

function extractServerAdminNameFromVCard(vcard) {
  const lines = serverAdminVCardLines(vcard)
  return firstProfileString(serverAdminVCardValue(lines, 'FN'), serverAdminVCardValue(lines, 'ORG'))
}

function serverAdminVCardLines(vcard) {
  return serverAdminUnfoldVCard(vcard)
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean)
}

function serverAdminUnfoldVCard(vcard) {
  return String(vcard ?? '').replace(/\r?\n[ \t]/g, '')
}

function serverAdminVCardValue(lines, fieldName) {
  const normalizedField = fieldName.toUpperCase()
  for (const line of lines) {
    const colon = line.indexOf(':')
    if (colon < 0) {
      continue
    }
    const name = line.slice(0, colon).split(';')[0].toUpperCase()
    if (name === normalizedField) {
      return unescapeServerAdminVCardValue(line.slice(colon + 1))
    }
  }
  return ''
}

function serverAdminVCardEmailAlias(lines, domain) {
  const suffix = `@${domain}`.toLowerCase()
  for (const line of lines) {
    const colon = line.indexOf(':')
    if (colon < 0 || !line.slice(0, colon).toUpperCase().startsWith('EMAIL')) {
      continue
    }
    const value = unescapeServerAdminVCardValue(line.slice(colon + 1))
    if (value.toLowerCase().endsWith(suffix)) {
      return value.slice(0, -suffix.length)
    }
  }
  return ''
}

function serverAdminVCardRelatedValues(lines, labelIncludes) {
  const labels = new Map()
  const values = []
  for (const line of lines) {
    const colon = line.indexOf(':')
    if (colon < 0) {
      continue
    }
    const name = line.slice(0, colon)
    const value = unescapeServerAdminVCardValue(line.slice(colon + 1))
    const match = name.match(/^item(\d+)\.X-AB(Label|RELATEDNAMES)$/i)
    if (!match) {
      continue
    }
    const item = match[1]
    if (match[2].toLowerCase() === 'label') {
      labels.set(item, value)
    } else if (String(labels.get(item) ?? '').toLowerCase().includes(labelIncludes.toLowerCase())) {
      values.push(value)
    }
  }
  return values
}

function unescapeServerAdminVCardValue(value) {
  return String(value ?? '')
    .replace(/\\n/g, '\n')
    .replace(/\\,/g, ',')
    .replace(/\\;/g, ';')
    .replace(/\\\\/g, '\\')
}

function serverAdminPermissionLabel(value) {
  const numeric = Number(value)
  if (Number.isFinite(numeric)) {
    return ['Untrusted', 'Limited', 'Standard', 'Trusted', 'Admin'][numeric] ?? String(value)
  }
  const level = serverAdminPermissionLevels.find(([key]) => key === String(value))
  return level ? level[1] : String(value || 'Unknown')
}

export default SettingsPage
