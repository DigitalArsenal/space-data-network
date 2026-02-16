import React, { useState, useEffect } from 'react'
import './EPMEditor.css'

const FIELDS = [
  { key: 'dn', label: 'Display Name', placeholder: 'My SDN Node' },
  { key: 'legal_name', label: 'Legal Name', placeholder: 'Acme Corp' },
  { key: 'given_name', label: 'Given Name', placeholder: 'Jane' },
  { key: 'family_name', label: 'Family Name', placeholder: 'Doe' },
  { key: 'additional_name', label: 'Middle Name', placeholder: '' },
  { key: 'honorific_prefix', label: 'Prefix', placeholder: 'Dr.' },
  { key: 'honorific_suffix', label: 'Suffix', placeholder: 'PhD' },
  { key: 'job_title', label: 'Job Title', placeholder: 'Network Engineer' },
  { key: 'occupation', label: 'Occupation', placeholder: 'Engineering' },
  { key: 'email', label: 'Email', placeholder: 'node@example.com' },
  { key: 'telephone', label: 'Telephone', placeholder: '+1-555-0100' }
]

const ADDRESS_FIELDS = [
  { key: 'street', label: 'Street', placeholder: '123 Main St' },
  { key: 'locality', label: 'City', placeholder: 'San Francisco' },
  { key: 'region', label: 'State/Region', placeholder: 'CA' },
  { key: 'postal_code', label: 'Postal Code', placeholder: '94105' },
  { key: 'country', label: 'Country', placeholder: 'US' },
  { key: 'po_box', label: 'PO Box', placeholder: '' }
]

const EPMEditor = ({ epm, onSave, saving }) => {
  const [form, setForm] = useState({})
  const [address, setAddress] = useState({})
  const [dirty, setDirty] = useState(false)

  useEffect(() => {
    if (!epm) return
    const f = {}
    FIELDS.forEach(({ key }) => { f[key] = epm[key] || '' })
    setForm(f)
    const a = {}
    ADDRESS_FIELDS.forEach(({ key }) => { a[key] = epm.address?.[key] || '' })
    setAddress(a)
    setDirty(false)
  }, [epm])

  const handleChange = (key, value) => {
    setForm(prev => ({ ...prev, [key]: value }))
    setDirty(true)
  }

  const handleAddressChange = (key, value) => {
    setAddress(prev => ({ ...prev, [key]: value }))
    setDirty(true)
  }

  const handleSubmit = (e) => {
    e.preventDefault()
    const profile = { ...form }
    const hasAddr = ADDRESS_FIELDS.some(({ key }) => address[key])
    if (hasAddr) {
      profile.address = { ...address }
    }
    onSave(profile)
    setDirty(false)
  }

  const handleReset = () => {
    if (!epm) return
    const f = {}
    FIELDS.forEach(({ key }) => { f[key] = epm[key] || '' })
    setForm(f)
    const a = {}
    ADDRESS_FIELDS.forEach(({ key }) => { a[key] = epm.address?.[key] || '' })
    setAddress(a)
    setDirty(false)
  }

  // Read-only identity fields from EPM
  const signingKey = epm?.keys?.find(k => k.key_type === 'signing')
  const encryptionKey = epm?.keys?.find(k => k.key_type === 'encryption')

  return (
    <div className='epm-editor'>
      <div className='epm-editor-section-title'>Edit Profile</div>

      <form onSubmit={handleSubmit}>
        <div className='epm-editor-grid'>
          {FIELDS.map(({ key, label, placeholder }) => (
            <div key={key} className='epm-editor-field'>
              <label className='epm-editor-label' htmlFor={`epm-${key}`}>{label}</label>
              <input
                id={`epm-${key}`}
                className='epm-editor-input'
                type={key === 'email' ? 'email' : key === 'telephone' ? 'tel' : 'text'}
                value={form[key] || ''}
                placeholder={placeholder}
                onChange={e => handleChange(key, e.target.value)}
              />
            </div>
          ))}
        </div>

        <div className='epm-editor-section-title' style={{ marginTop: 20 }}>Address</div>
        <div className='epm-editor-grid'>
          {ADDRESS_FIELDS.map(({ key, label, placeholder }) => (
            <div key={key} className='epm-editor-field'>
              <label className='epm-editor-label' htmlFor={`epm-addr-${key}`}>{label}</label>
              <input
                id={`epm-addr-${key}`}
                className='epm-editor-input'
                type='text'
                value={address[key] || ''}
                placeholder={placeholder}
                onChange={e => handleAddressChange(key, e.target.value)}
              />
            </div>
          ))}
        </div>

        {/* Read-only identity section */}
        {(signingKey || encryptionKey || epm?.peer_id) && (
          <>
            <div className='epm-editor-section-title' style={{ marginTop: 20 }}>Identity (read-only)</div>
            <div className='epm-editor-readonly'>
              {epm?.peer_id && (
                <div className='epm-editor-ro-field'>
                  <span className='epm-editor-ro-label'>Peer ID</span>
                  <span className='epm-editor-ro-value'>{epm.peer_id}</span>
                </div>
              )}
              {signingKey?.xpub && (
                <div className='epm-editor-ro-field'>
                  <span className='epm-editor-ro-label'>XPub</span>
                  <span className='epm-editor-ro-value'>{signingKey.xpub}</span>
                </div>
              )}
              {signingKey?.public_key && (
                <div className='epm-editor-ro-field'>
                  <span className='epm-editor-ro-label'>Signing Key</span>
                  <span className='epm-editor-ro-value'>{signingKey.public_key}</span>
                </div>
              )}
              {encryptionKey?.public_key && (
                <div className='epm-editor-ro-field'>
                  <span className='epm-editor-ro-label'>Encryption Key</span>
                  <span className='epm-editor-ro-value'>{encryptionKey.public_key}</span>
                </div>
              )}
            </div>
          </>
        )}

        <div className='epm-editor-actions'>
          <button
            type='submit'
            className='epm-editor-btn epm-editor-btn-primary'
            disabled={!dirty || saving}
          >
            {saving ? 'Saving...' : 'Save Profile'}
          </button>
          <button
            type='button'
            className='epm-editor-btn'
            onClick={handleReset}
            disabled={!dirty || saving}
          >
            Reset
          </button>
        </div>
      </form>
    </div>
  )
}

export default EPMEditor
