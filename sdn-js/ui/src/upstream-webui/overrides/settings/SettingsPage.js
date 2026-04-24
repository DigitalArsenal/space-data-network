import React, { useEffect, useRef, useState } from 'react'
import Box from '../../../../../../webui/src/components/box/Box.js'
import Button from '../../../../../../webui/src/components/button/button.tsx'
import Title from '../../../../../../webui/src/settings/Title.js'
import UpstreamSettingsPage from '../../../../../../webui/src/settings/LoadableSettingsPage.js'

const textFields = [
  ['dn', 'Display name'],
  ['legal_name', 'Legal name'],
  ['given_name', 'Given name'],
  ['family_name', 'Family name'],
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

const profileGridStyle = {
  display: 'grid',
  gridTemplateColumns: 'repeat(auto-fit, minmax(16rem, 1fr))',
  columnGap: '1rem',
}

function SettingsPage() {
  return (
    <div className='mw9 center ph3 ph4-l'>
      <NodeProfileSection />
      <UpstreamSettingsPage />
    </div>
  )
}

function NodeProfileSection() {
  const fileRef = useRef(null)
  const videoRef = useRef(null)
  const streamRef = useRef(null)
  const [profile, setProfile] = useState(emptyProfile)
  const [status, setStatus] = useState('loading')
  const [error, setError] = useState(null)
  const [cameraOpen, setCameraOpen] = useState(false)
  const [qrVersion, setQrVersion] = useState(0)

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
      setQrVersion((value) => value + 1)
      setStatus('saved')
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setStatus('error')
    }
  }

  async function handlePhotoFile(event) {
    const file = event.target.files?.[0]
    if (!file) {
      return
    }
    try {
      const dataUrl = await readFileAsDataURL(file)
      setProfile((current) => ({ ...current, photo_data_url: dataUrl }))
    } finally {
      event.target.value = ''
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
            ? <img className='br-100 ba b--black-10 bg-snow-muted object-cover' alt='Node profile' src={profile.photo_data_url} style={{ width: 96, height: 96 }} />
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
                  onChange={(event) => setProfile((current) => ({ ...current, [key]: event.target.value }))}
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
                  onChange={(event) => setProfile((current) => ({
                    ...current,
                    address: {
                      ...(current.address ?? {}),
                      [key]: event.target.value,
                    },
                  }))}
                />
              </label>
            ))}
          </div>
        </div>

        <aside className='w-100 w-third-l pl0 pl4-l bl-l b--black-10'>
          <h3 className='f5 mt0 mb2 charcoal'>Photo</h3>
          <input ref={fileRef} className='dn' type='file' accept='image/*' onChange={handlePhotoFile} />
          <div className='flex flex-column'>
            <Button minWidth={180} className='mb2' buttonType='button' onClick={() => fileRef.current?.click()}>
              Upload image
            </Button>
            <Button minWidth={180} bg='bg-white' color='blue' fill='fill-blue' className='mb2 ba b--black-20' buttonType='button' onClick={openCamera}>
              Take picture
            </Button>
            {profile.photo_data_url && (
              <Button minWidth={180} bg='bg-white' color='red' fill='fill-red' className='mb2 ba b--black-20' buttonType='button' onClick={() => setProfile((current) => ({ ...current, photo_data_url: '' }))}>
                Remove image
              </Button>
            )}
          </div>

          {cameraOpen && (
            <div className='mt3 pa2 ba b--black-10 br2 bg-snow-muted'>
              <video ref={videoRef} className='db w-100 bg-charcoal' playsInline muted />
              <div className='mt2 flex'>
                <Button minWidth={120} className='mr2' buttonType='button' onClick={capturePhoto}>Capture</Button>
                <Button minWidth={100} bg='bg-white' color='blue' fill='fill-blue' className='ba b--black-20' buttonType='button' onClick={closeCamera}>Cancel</Button>
              </div>
            </div>
          )}

          <h3 className='f5 mt4 mb2 charcoal'>Share</h3>
          <div className='flex flex-column'>
            <a className='Button transition-all sans-serif dib v-mid fw5 nowrap lh-copy bn br1 pa2 focus-outline bg-teal white fill-white tc link mb2' style={{ minWidth: 180 }} href='/api/node/epm/vcard' download='sdn-node.vcf'>
              Download .vcf
            </a>
            <div className='pa2 ba b--black-10 br2 bg-white tc'>
              <img alt='Node profile QR code' src={`/api/node/epm/qr?v=${qrVersion}`} style={{ maxWidth: '100%', width: 180 }} />
            </div>
          </div>
        </aside>
      </div>

      <div className='mt3 flex flex-column flex-row-ns items-start items-center-ns justify-between-ns'>
        <div className='f6 black-60 mb3 mb0-ns'>
          {status === 'loading' ? 'Loading node profile...' : status === 'saving' ? 'Saving profile...' : status === 'saved' ? 'Profile saved.' : 'Profile is loaded from the node EPM.'}
          {error && <span className='dark-red ml0 ml2-ns db dib-ns'>{error}</span>}
        </div>
        <Button minWidth={140} disabled={status === 'saving'} onClick={saveProfile} buttonType='button'>
          Save profile
        </Button>
      </div>
    </Box>
  )
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

export default SettingsPage
