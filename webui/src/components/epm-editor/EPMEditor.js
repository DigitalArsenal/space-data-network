import React, { useState, useEffect } from 'react'
import './EPMEditor.css'

const FIELDS = [
  { key: 'dn', label: 'Display Name', placeholder: 'My SDN Node', span: 'full' },
  { key: 'legal_name', label: 'Legal Name', placeholder: 'Acme Corp', span: 'full' },
  { key: 'given_name', label: 'Given Name', placeholder: 'Jane' },
  { key: 'family_name', label: 'Family Name', placeholder: 'Doe' },
  { key: 'additional_name', label: 'Middle Name', placeholder: '' },
  { key: 'honorific_prefix', label: 'Prefix', placeholder: 'Dr.', span: 'third' },
  { key: 'honorific_suffix', label: 'Suffix', placeholder: 'PhD', span: 'third' },
  { key: 'job_title', label: 'Job Title', placeholder: 'Network Engineer' },
  { key: 'occupation', label: 'Occupation', placeholder: 'Engineering' },
  { key: 'email', label: 'Email', placeholder: 'node@example.com', span: 'full' },
  { key: 'telephone', label: 'Telephone', placeholder: '+44 20 7946 0958' }
]

const ADDRESS_FIELDS = [
  { key: 'street', label: 'Street', placeholder: '123 Main St', span: 'full' },
  { key: 'locality', label: 'City', placeholder: 'San Francisco' },
  { key: 'region', label: 'State/Region', placeholder: 'CA' },
  { key: 'postal_code', label: 'Postal Code', placeholder: '94105', span: 'third' },
  { key: 'country', label: 'Country', placeholder: 'US', span: 'third' },
  { key: 'po_box', label: 'PO Box', placeholder: '', span: 'third' }
]

const EPMEditor = ({ epm, onSave, saving }) => {
  const [form, setForm] = useState({})
  const [address, setAddress] = useState({})
  const [dirty, setDirty] = useState(false)
  const [errors, setErrors] = useState({})

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
    if (errors[key]) setErrors(prev => { const n = { ...prev }; delete n[key]; return n })
  }

  const handleAddressChange = (key, value) => {
    setAddress(prev => ({ ...prev, [key]: value }))
    setDirty(true)
    if (errors[`addr_${key}`]) setErrors(prev => { const n = { ...prev }; delete n[`addr_${key}`]; return n })
  }

  const handleSubmit = () => {
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

  const validate = () => {
    const errs = {}

    // Display name: required
    if (!form.dn?.trim()) {
      errs.dn = 'Display name is required'
    }

    // Email: basic RFC 5322 local + domain check, supports international domains
    if (form.email?.trim()) {
      // Allows unicode in local part and domain (internationalized email)
      if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(form.email.trim())) {
        errs.email = 'Invalid email address'
      }
    }

    // Telephone: E.164-ish — allow +, digits, spaces, hyphens, parens, dots
    if (form.telephone?.trim()) {
      if (!/^\+?[\d\s\-().]{4,20}$/.test(form.telephone.trim())) {
        errs.telephone = 'Use international format, e.g. +44 20 7946 0958'
      }
    }

    // Prefix: short honorific
    if (form.honorific_prefix?.trim() && form.honorific_prefix.trim().length > 20) {
      errs.honorific_prefix = 'Too long'
    }

    // Suffix: short honorific
    if (form.honorific_suffix?.trim() && form.honorific_suffix.trim().length > 20) {
      errs.honorific_suffix = 'Too long'
    }

    // Postal code: allow international formats (letters, digits, spaces, hyphens)
    if (address.postal_code?.trim()) {
      if (!/^[A-Za-z0-9\s-]{2,12}$/.test(address.postal_code.trim())) {
        errs.addr_postal_code = 'Invalid postal code'
      }
    }

    // Country: 2-letter ISO or full name
    if (address.country?.trim()) {
      const c = address.country.trim()
      if (c.length === 1 || c.length > 60) {
        errs.addr_country = 'Use ISO code (e.g. US, GB) or full name'
      }
    }

    setErrors(errs)
    return Object.keys(errs).length === 0
  }

  return (
    <div className='epm-editor'>
      <div className='epm-editor-section-title'>Edit Profile</div>

      <form onSubmit={(e) => { e.preventDefault(); if (validate()) { handleSubmit() } }}>
        <div className='epm-editor-grid'>
          {FIELDS.map(({ key, label, placeholder, span }) => (
            <div key={key} className={`epm-editor-field${span === 'full' ? ' epm-editor-field-full' : span === 'third' ? ' epm-editor-field-third' : ''}`}>
              <label className='epm-editor-label' htmlFor={`epm-${key}`}>{label}</label>
              <input
                id={`epm-${key}`}
                className={`epm-editor-input${errors[key] ? ' epm-editor-input-error' : ''}`}
                type={key === 'email' ? 'email' : key === 'telephone' ? 'tel' : 'text'}
                value={form[key] || ''}
                placeholder={placeholder}
                onChange={e => handleChange(key, e.target.value)}
                maxLength={key === 'honorific_prefix' || key === 'honorific_suffix' ? 20 : key === 'email' ? 254 : 100}
              />
              {errors[key] && <span className='epm-editor-error'>{errors[key]}</span>}
            </div>
          ))}
        </div>

        <div className='epm-editor-section-title' style={{ marginTop: 20 }}>Address</div>
        <div className='epm-editor-grid'>
          {ADDRESS_FIELDS.map(({ key, label, placeholder, span }) => (
            <div key={key} className={`epm-editor-field${span === 'full' ? ' epm-editor-field-full' : span === 'third' ? ' epm-editor-field-third' : ''}`}>
              <label className='epm-editor-label' htmlFor={`epm-addr-${key}`}>{label}</label>
              <input
                id={`epm-addr-${key}`}
                className={`epm-editor-input${errors[`addr_${key}`] ? ' epm-editor-input-error' : ''}`}
                type='text'
                value={address[key] || ''}
                placeholder={placeholder}
                onChange={e => handleAddressChange(key, e.target.value)}
              />
              {errors[`addr_${key}`] && <span className='epm-editor-error'>{errors[`addr_${key}`]}</span>}
            </div>
          ))}
        </div>

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
