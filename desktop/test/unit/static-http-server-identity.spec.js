const fs = require('fs')
const os = require('os')
const path = require('path')
const { EventEmitter } = require('events')
const { test, expect } = require('@playwright/test')
const proxyquire = require('proxyquire').noCallThru()

const HD_TEST_XPUB = 'xpub6BpyEDT14VWygfxLMawQKhGXLCVMhJK7voSnjD7VsYYzUfQb6vbTwNhDbXwsa5KraQQgfpDzTq45TfdXQzNiFRfGoFpgbd9KymJsauL4MuT'
const HD_TEST_SIGNING_PUBLIC_KEY = '0321fce2a66e6c1be09128b20e3f50374fa05ec1ceb84eaa78e69cf1cddc60a7a6'
const HD_TEST_ENCRYPTION_PUBLIC_KEY = '0301f6e5f01a7765617c817568db07e81dc1b86a87575f4702f347b5897f6b1d06'

test.describe('desktop static identity API', () => {
  test('stores hosted EPM records encrypted at rest and serves public exports', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-identity-api-'))
    const { serveDesktopIdentityAPI } = loadStaticServer(userData)

    const put = await requestJson(serveDesktopIdentityAPI, 'PUT', '/api/identity/epms/alice', {
      epm_json: {
        dn: 'Dr. Alice Q. Example',
        legal_name: 'Example Orbital LLC',
        given_name: 'Alice',
        family_name: 'Example',
        additional_name: 'Q.',
        honorific_prefix: 'Dr.',
        honorific_suffix: 'PhD',
        email: 'alice@example.com',
        telephone: '+1 555 0100',
        job_title: 'Flight Director',
        occupation: 'Operator',
        address: {
          po_box: 'Box 42',
          street: '1 Orbit Way',
          locality: 'Cape Canaveral',
          region: 'FL',
          postal_code: '32920',
          country: 'USA'
        },
        peer_id: '16Uiu2Alice',
        epm_cid: 'bafy-alice-epm',
        public_key: 'abcdef',
        signing_public_key: 'signing-public',
        encryption_public_key: 'encryption-public',
        private_key: 'must-not-be-exported'
      }
    })

    expect(put.statusCode).toBe(200)
    expect(put.json).toMatchObject({
      id: 'alice',
      kind: 'hosted',
      label: 'Dr. Alice Q. Example',
      peerId: '16Uiu2Alice'
    })

    const diskContents = fs.readdirSync(userData)
      .map(name => fs.readFileSync(path.join(userData, name), 'utf8'))
      .join('\n')
    expect(diskContents).not.toContain('Dr. Alice Q. Example')
    expect(diskContents).not.toContain('must-not-be-exported')

    const list = await requestJson(serveDesktopIdentityAPI, 'GET', '/api/identity/epms')
    expect(list.statusCode).toBe(200)
    expect(list.json.epms).toEqual(expect.arrayContaining([
      expect.objectContaining({ id: 'self', kind: 'node-self' }),
      expect.objectContaining({ id: 'alice', label: 'Dr. Alice Q. Example' })
    ]))

    const read = await requestJson(serveDesktopIdentityAPI, 'GET', '/api/identity/epms/alice')
    expect(read.statusCode).toBe(200)
    expect(read.body).toContain('Dr. Alice Q. Example')
    expect(read.body).not.toContain('must-not-be-exported')

    const vcard = await requestRaw(serveDesktopIdentityAPI, 'GET', '/api/identity/epms/alice/vcard')
    expect(vcard.statusCode).toBe(200)
    const unfoldedVcard = vcard.body.replace(/\r\n[ \t]/g, '')
    expect(vcard.body).toContain('BEGIN:VCARD')
    expect(vcard.body).toContain('N:Example;Alice;Q.;Dr.;PhD')
    expect(vcard.body).toContain('FN:Dr. Alice Q. Example')
    expect(vcard.body).toContain('ORG:Example Orbital LLC')
    expect(vcard.body).toContain('EMAIL;TYPE=INTERNET:alice@example.com')
    expect(vcard.body).toContain('TEL:+1 555 0100')
    expect(vcard.body).toContain('TITLE:Flight Director')
    expect(vcard.body).toContain('ROLE:Operator')
    expect(vcard.body).toContain('ADR;TYPE=WORK:Box 42;;1 Orbit Way;Cape Canaveral;FL;32920;USA')
    expect(vcard.body).toContain('EMAIL;TYPE=INTERNET:abcdef@spacedatanetwork.org')
    expect(unfoldedVcard).toContain('EMAIL;type=INTERNET;type=signing:signing-public@signing.spacedatanetwork.org')
    expect(unfoldedVcard).toContain('EMAIL;type=INTERNET;type=encryption:encryption-public@encryption.spacedatanetwork.org')
    expect(vcard.body).toContain('X-SDN-PEER-ID:16Uiu2Alice')
    expect(vcard.body).toContain('X-SDN-PUBLIC-KEY:abcdef')
    expect(vcard.body).toContain('X-SDN-SIGNING-PUBLIC-KEY:signing-public')
    expect(vcard.body).toContain('X-SDN-ENCRYPTION-PUBLIC-KEY:encryption-public')
    expect(vcard.body).not.toContain('must-not-be-exported')
  })

  test('serves local node and person directory search from public hosted EPMs', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-directory-api-'))
    const { serveDesktopDirectoryAPI, serveDesktopIdentityAPI } = loadStaticServer(userData)

    await requestJson(serveDesktopIdentityAPI, 'PUT', '/api/identity/epms/alice', {
      epm_json: {
        dn: 'Alice Operator',
        entity_type: 'Person',
        peer_id: '16Uiu2Alice',
        public_key: 'abcdef',
        private_key: 'must-not-be-indexed'
      }
    })
    await requestJson(serveDesktopIdentityAPI, 'PUT', '/api/identity/epms/provider-node', {
      epm_json: {
        dn: 'CelesTrak Provider',
        entity_type: 'Node',
        peer_id: '16Uiu2Provider',
        public_key: '123456',
        private_key: 'must-not-be-indexed'
      }
    })

    const nodes = await requestJson(serveDesktopDirectoryAPI, 'GET', '/api/directory/nodes?q=provider')
    expect(nodes.statusCode).toBe(200)
    expect(nodes.json.nodes).toEqual(expect.arrayContaining([
      expect.objectContaining({ dn: 'CelesTrak Provider', peer_id: '16Uiu2Provider' })
    ]))
    expect(nodes.body).not.toContain('must-not-be-indexed')

    const users = await requestJson(serveDesktopDirectoryAPI, 'GET', '/api/directory/users?q=alice')
    expect(users.statusCode).toBe(200)
    expect(users.json.users).toEqual(expect.arrayContaining([
      expect.objectContaining({ dn: 'Alice Operator', peer_id: '16Uiu2Alice' })
    ]))
    expect(users.body).not.toContain('must-not-be-indexed')
  })

  test('serves peer EPM and vCard artifacts from hosted directory records', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-peer-epm-api-'))
    const { serveDesktopIdentityAPI, serveDesktopPeerEPMAPI } = loadStaticServer(userData)

    await requestJson(serveDesktopIdentityAPI, 'PUT', '/api/identity/epms/provider', {
      epm_json: {
        dn: 'Provider Node',
        email: 'provider@example.invalid',
        entity_type: 'Node',
        peer_id: '16Uiu2ProviderPeer',
        public_key: 'provider-public',
        signing_public_key: 'provider-signing',
        encryption_public_key: 'provider-encryption'
      }
    })

    const epm = await requestRaw(serveDesktopPeerEPMAPI, 'GET', '/api/peers/16Uiu2ProviderPeer/epm')
    expect(epm.statusCode).toBe(200)
    expect(epm.headers['Content-Type']).toBe('application/x-flatbuffers')

    const vcard = await requestRaw(serveDesktopPeerEPMAPI, 'GET', '/api/peers/16Uiu2ProviderPeer/epm/vcard')
    expect(vcard.statusCode).toBe(200)
    expect(vcard.headers['Content-Type']).toContain('text/vcard')
    expect(vcard.body).toContain('FN:Provider Node')
    expect(vcard.body).toContain('X-SDN-PEER-ID:16Uiu2ProviderPeer')

    const missing = await requestRaw(serveDesktopPeerEPMAPI, 'GET', '/api/peers/16Uiu2Missing/epm')
    expect(missing.statusCode).toBe(404)
  })

  test('accepts local desktop auth user create and update routes used by settings override', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-auth-users-api-'))
    const { serveDesktopAuthUsersAPI } = loadStaticServer(userData)

    // Task F1: the desktop-only 'local' trust_level is retired in favor of
    // the canonical PGP scale (matches sdn-server's peers.TrustLevel). A
    // caller may still submit the legacy value (e.g. an older settings
    // override build) but the store normalizes it to 'admin' on write.
    const grant = {
      xpub: 'xpub-desktop-admin',
      label: 'Desktop Admin',
      role: 'admin',
      trust_level: 'local'
    }

    const created = await requestJson(serveDesktopAuthUsersAPI, 'POST', '/api/auth/users', grant)
    expect(created.statusCode).toBe(200)
    expect(created.json.user).toMatchObject({ ...grant, trust_level: 'admin' })

    const conflict = await requestJson(serveDesktopAuthUsersAPI, 'POST', '/api/auth/users', grant)
    expect(conflict.statusCode).toBe(409)

    const updated = await requestJson(serveDesktopAuthUsersAPI, 'PUT', '/api/auth/users/xpub-desktop-admin', {
      ...grant,
      label: 'Updated Desktop Admin'
    })
    expect(updated.statusCode).toBe(200)
    expect(updated.json.user.label).toBe('Updated Desktop Admin')

    const listed = await requestJson(serveDesktopAuthUsersAPI, 'GET', '/api/auth/users')
    expect(listed.statusCode).toBe(200)
    expect(listed.json.users).toEqual([
      expect.objectContaining({ xpub: 'xpub-desktop-admin', label: 'Updated Desktop Admin', trust_level: 'admin' })
    ])
  })

  test('registers an xpub user with a canonical PGP trust level identically to the server vocabulary', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-auth-users-canonical-'))
    const { serveDesktopAuthUsersAPI } = loadStaticServer(userData)

    // sdn-server's internal/peers.TrustLevel.String() emits exactly these
    // names; the desktop settings override (sdn-js/ui) already submits them
    // directly (see SettingsPage.js's permissionLevel select). Round-trip
    // one to confirm the desktop store passes canonical values through
    // unchanged instead of coercing them into desktop-only vocabulary.
    const grant = {
      xpub: 'xpub-desktop-standard-user',
      label: 'Canonical Trust User',
      role: 'operator',
      trust_level: 'standard'
    }

    const created = await requestJson(serveDesktopAuthUsersAPI, 'POST', '/api/auth/users', grant)
    expect(created.statusCode).toBe(200)
    expect(created.json.user).toMatchObject(grant)

    const listed = await requestJson(serveDesktopAuthUsersAPI, 'GET', '/api/auth/users')
    expect(listed.statusCode).toBe(200)
    expect(listed.json.users).toEqual([
      expect.objectContaining({ xpub: 'xpub-desktop-standard-user', trust_level: 'standard' })
    ])
  })

  test('migrates a pre-F1 desktop-auth-users.json trust_level vocabulary on read', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-auth-users-migrate-'))
    fs.writeFileSync(path.join(userData, 'desktop-auth-users.json'), JSON.stringify({
      users: [
        { xpub: 'xpub-legacy-local', label: 'Legacy Local Admin', role: 'admin', trust_level: 'local' },
        { xpub: 'xpub-legacy-blank', label: 'Legacy Blank', role: 'admin', trust_level: '' },
        { xpub: 'xpub-already-canonical', label: 'Already Canonical', role: 'admin', trust_level: 'ultimate' }
      ]
    }))
    const { serveDesktopAuthUsersAPI } = loadStaticServer(userData)

    const listed = await requestJson(serveDesktopAuthUsersAPI, 'GET', '/api/auth/users')
    expect(listed.statusCode).toBe(200)
    expect(listed.json.users).toEqual(expect.arrayContaining([
      expect.objectContaining({ xpub: 'xpub-legacy-local', trust_level: 'admin' }),
      expect.objectContaining({ xpub: 'xpub-legacy-blank', trust_level: 'admin' }),
      expect.objectContaining({ xpub: 'xpub-already-canonical', trust_level: 'ultimate' })
    ]))
  })

  test('serves the local node EPM route as a raw FlatBuffer', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-node-epm-api-'))
    const { serveDesktopNodeEPMAPI } = loadStaticServer(userData)

    const put = await requestRaw(serveDesktopNodeEPMAPI, 'PUT', '/api/node/epm', JSON.stringify({
      dn: 'Desktop Node',
      email: 'node@example.invalid',
      peer_id: '12D3KooWDesktopNode',
      signing_public_key: 'ed25519-node-signing-public',
      encryption_public_key: 'x25519-node-encryption-public'
    }))
    expect(put.statusCode).toBe(200)
    expect(put.headers['Content-Type']).toBe('application/x-flatbuffers')

    const get = await requestRaw(serveDesktopNodeEPMAPI, 'GET', '/api/node/epm')
    expect(get.statusCode).toBe(200)
    expect(get.headers['Content-Type']).toBe('application/x-flatbuffers')

    const flatbuffers = await import('flatbuffers')
    const { EPM } = await import('spacedatastandards.org/lib/js/EPM/EPM.js')
    const epm = EPM.getSizePrefixedRootAsEPM(new flatbuffers.ByteBuffer(new Uint8Array(get.bodyBuffer)))
    expect(epm.DN()).toBe('Desktop Node')
    expect(epm.EMAIL()).toBe('node@example.invalid')
    expect(epm.keysLength()).toBe(2)
    expect(epm.KEYS(0).PUBLIC_KEY()).toBe('ed25519-node-signing-public')
    expect(epm.KEYS(1).PUBLIC_KEY()).toBe('x25519-node-encryption-public')

    const vcard = await requestRaw(serveDesktopNodeEPMAPI, 'GET', '/api/node/epm/vcard')
    expect(vcard.statusCode).toBe(200)
    expect(vcard.headers['Content-Type']).toContain('text/vcard')
    expect(vcard.body).toContain('BEGIN:VCARD')
    expect(vcard.body).toContain('FN:Desktop Node')
    expect(vcard.body).toContain('EMAIL;TYPE=INTERNET:node@example.invalid')
    expect(vcard.body).toContain('X-SDN-PEER-ID:12D3KooWDesktopNode')
    expect(vcard.body).toContain('X-SDN-SIGNING-PUBLIC-KEY:ed25519-node-signing-public')
    expect(vcard.body).toContain('X-SDN-ENCRYPTION-PUBLIC-KEY:x25519-node-encryption-public')
  })

  test('persists node identity settings and blocks wallet key replacement until confirmed', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-node-identity-settings-'))
    const { serveDesktopNodeIdentityAPI } = loadStaticServer(userData)

    const defaultSettings = await requestJson(serveDesktopNodeIdentityAPI, 'GET', '/api/node/identity/settings')
    expect(defaultSettings.statusCode).toBe(200)
    expect(defaultSettings.json.ttl_ms).toBe(3600000)
    expect(defaultSettings.json.flatbuffer_storage_path).toBe(path.join(userData, 'flatbuffers'))
    expect(fs.existsSync(path.join(userData, 'flatbuffers'))).toBe(true)

    const savedSettings = await requestJson(serveDesktopNodeIdentityAPI, 'PUT', '/api/node/identity/settings', {
      ttl_ms: 900000,
      flatbuffer_storage_path: path.join(userData, 'custom-flatbuffers')
    })
    expect(savedSettings.statusCode).toBe(200)
    expect(savedSettings.json.ttl_ms).toBe(900000)
    expect(savedSettings.json.flatbuffer_storage_path).toBe(path.join(userData, 'custom-flatbuffers'))

    const first = await requestJson(serveDesktopNodeIdentityAPI, 'PUT', '/api/node/identity/wallet', {
      wallet_identity: {
        peer_id: '12D3KooWFirst',
        wallet_account_id: 'wallet-0',
        wallet_account_label: 'Wallet 1',
        xpub: 'xpub-first',
        identity_public_key: '11'.repeat(33),
        signing_public_key: 'aa'.repeat(32),
        encryption_public_key: 'bb'.repeat(32),
        signature: 'cc'.repeat(64),
        signature_payload: 'payload-first',
        signature_timestamp: 1778700000
      }
    })
    expect(first.statusCode).toBe(200)
    expect(first.json.status).toBe('updated')
    expect(first.json.profile.peer_id).toBe('12D3KooWFirst')

    const settingsWithSession = await requestJson(serveDesktopNodeIdentityAPI, 'GET', '/api/node/identity/settings')
    expect(settingsWithSession.statusCode).toBe(200)
    expect(settingsWithSession.json.session).toMatchObject({
      unlocked: true,
      profile: expect.objectContaining({
        peer_id: '12D3KooWFirst',
        signing_public_key: 'aa'.repeat(32)
      })
    })
    expect(typeof settingsWithSession.json.session.expires_at).toBe('string')
    expect(fs.readFileSync(path.join(userData, 'sdn-node-identity-session.json'), 'utf8')).toContain('12D3KooWFirst')

    const session = await requestJson(serveDesktopNodeIdentityAPI, 'GET', '/api/node/identity/session')
    expect(session.statusCode).toBe(200)
    expect(session.json).toMatchObject({ unlocked: true })

    const logout = await requestJson(serveDesktopNodeIdentityAPI, 'DELETE', '/api/node/identity/session')
    expect(logout.statusCode).toBe(200)
    expect(logout.json).toMatchObject({ unlocked: false })

    const settingsAfterLogout = await requestJson(serveDesktopNodeIdentityAPI, 'GET', '/api/node/identity/settings')
    expect(settingsAfterLogout.statusCode).toBe(200)
    expect(settingsAfterLogout.json.session).toMatchObject({ unlocked: false })

    const mismatch = await requestJson(serveDesktopNodeIdentityAPI, 'PUT', '/api/node/identity/wallet', {
      wallet_identity: {
        peer_id: '12D3KooWSecond',
        wallet_account_id: 'wallet-1',
        wallet_account_label: 'Wallet 2',
        xpub: 'xpub-second',
        identity_public_key: '22'.repeat(33),
        signing_public_key: 'dd'.repeat(32),
        encryption_public_key: 'ee'.repeat(32),
        signature: 'ff'.repeat(64),
        signature_payload: 'payload-second',
        signature_timestamp: 1778700001
      }
    })
    expect(mismatch.statusCode).toBe(409)
    expect(mismatch.json.status).toBe('mismatch')
    expect(mismatch.json.current.peer_id).toBe('12D3KooWFirst')
    expect(mismatch.json.proposed.peer_id).toBe('12D3KooWSecond')

    const confirmed = await requestJson(serveDesktopNodeIdentityAPI, 'PUT', '/api/node/identity/wallet', {
      replace: true,
      wallet_identity: {
        peer_id: '12D3KooWSecond',
        wallet_account_id: 'wallet-1',
        wallet_account_label: 'Wallet 2',
        xpub: 'xpub-second',
        identity_public_key: '22'.repeat(33),
        signing_public_key: 'dd'.repeat(32),
        encryption_public_key: 'ee'.repeat(32),
        signature: 'ff'.repeat(64),
        signature_payload: 'payload-second',
        signature_timestamp: 1778700001
      }
    })
    expect(confirmed.statusCode).toBe(200)
    expect(confirmed.json.status).toBe('updated')
    expect(confirmed.json.profile.peer_id).toBe('12D3KooWSecond')
  })

  test('selects a FlatBuffer storage location through the desktop directory picker', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-flatbuffer-location-'))
    const currentPath = path.join(userData, 'current-flatbuffers')
    const selectedPath = path.join(userData, 'selected-flatbuffers')
    const dialog = {
      showOpenDialog: async (options) => {
        expect(options).toMatchObject({
          title: 'Select FlatBuffer storage location',
          defaultPath: currentPath,
          properties: ['openDirectory', 'createDirectory']
        })
        return { canceled: false, filePaths: [selectedPath] }
      }
    }
    const { serveDesktopNodeIdentityAPI } = loadStaticServer(userData, { dialog })

    const picked = await requestJson(serveDesktopNodeIdentityAPI, 'POST', '/api/node/identity/settings/flatbuffer-storage-location', {
      current_path: currentPath
    })

    expect(picked.statusCode).toBe(200)
    expect(picked.json).toEqual({
      canceled: false,
      path: selectedPath
    })
  })

  test('stores FlatSQL persistence blobs inside the configured FlatBuffer storage directory', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-flatsql-persistence-'))
    const storagePath = path.join(userData, 'flatbuffer-store')
    const { serveDesktopNodeIdentityAPI, serveDesktopFlatSqlPersistenceAPI } = loadStaticServer(userData)

    await requestJson(serveDesktopNodeIdentityAPI, 'PUT', '/api/node/identity/settings', {
      ttl_ms: 900000,
      flatbuffer_storage_path: storagePath
    })

    const key = 'sdn-data:configured:space-data-network-02:OMM'
    const encodedKey = encodeURIComponent(key)
    const bytes = Buffer.from([1, 2, 3, 4, 5])
    const saved = await requestRaw(
      serveDesktopFlatSqlPersistenceAPI,
      'PUT',
      `/api/flatsql/persistence/${encodedKey}`,
      bytes,
      { 'content-type': 'application/octet-stream' }
    )
    expect(saved.statusCode).toBe(204)

    const loaded = await requestRaw(serveDesktopFlatSqlPersistenceAPI, 'GET', `/api/flatsql/persistence/${encodedKey}`)
    expect(loaded.statusCode).toBe(200)
    expect(loaded.headers['Content-Type']).toBe('application/octet-stream')
    expect(loaded.bodyBuffer).toEqual(bytes)

    const files = fs.readdirSync(storagePath)
    expect(files).toHaveLength(1)
    expect(files[0]).toMatch(/^sdn-data_configured_space-data-network-02_OMM-[a-f0-9]{16}\.bin$/)

    const deleted = await requestRaw(serveDesktopFlatSqlPersistenceAPI, 'DELETE', `/api/flatsql/persistence/${encodedKey}`)
    expect(deleted.statusCode).toBe(204)
    const missing = await requestRaw(serveDesktopFlatSqlPersistenceAPI, 'GET', `/api/flatsql/persistence/${encodedKey}`)
    expect(missing.statusCode).toBe(404)
  })

  test('persists hd-wallet localStorage entries encrypted at rest for wallet remember flows', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-wallet-storage-'))
    const { serveDesktopNodeIdentityAPI } = loadStaticServer(userData)

    const saved = await requestJson(serveDesktopNodeIdentityAPI, 'PUT', '/api/node/identity/wallet-storage', {
      entries: {
        wallet_storage_metadata: '{"method":"passkey","version":3}',
        wallet_storage_encrypted: '{"ciphertext":"secret-wallet"}',
        wallet_storage_passkey_credential: '{"id":"credential-id","hasPRF":true}',
        'hd-wallet-wallets': '[{"id":0,"name":"Operations"}]',
        'not-wallet-state': 'must-not-be-stored'
      }
    })
    expect(saved.statusCode).toBe(200)
    expect(saved.json.entries).toMatchObject({
      wallet_storage_metadata: '{"method":"passkey","version":3}',
      wallet_storage_encrypted: '{"ciphertext":"secret-wallet"}',
      wallet_storage_passkey_credential: '{"id":"credential-id","hasPRF":true}',
      'hd-wallet-wallets': '[{"id":0,"name":"Operations"}]'
    })
    expect(saved.json.entries['not-wallet-state']).toBeUndefined()
    expect(saved.json.encrypted_at_rest).toBe(true)

    const diskContents = fs.readdirSync(userData)
      .map(name => fs.readFileSync(path.join(userData, name), 'utf8'))
      .join('\n')
    expect(diskContents).not.toContain('secret-wallet')
    expect(diskContents).not.toContain('credential-id')
    expect(diskContents).not.toContain('Operations')

    const loaded = await requestJson(serveDesktopNodeIdentityAPI, 'GET', '/api/node/identity/wallet-storage')
    expect(loaded.statusCode).toBe(200)
    expect(loaded.json.entries).toMatchObject({
      wallet_storage_metadata: '{"method":"passkey","version":3}',
      wallet_storage_encrypted: '{"ciphertext":"secret-wallet"}',
      wallet_storage_passkey_credential: '{"id":"credential-id","hasPRF":true}',
      'hd-wallet-wallets': '[{"id":0,"name":"Operations"}]'
    })

    const removed = await requestJson(serveDesktopNodeIdentityAPI, 'PUT', '/api/node/identity/wallet-storage', {
      entries: {
        wallet_storage_encrypted: null,
        'hd-wallet-wallets': null
      }
    })
    expect(removed.statusCode).toBe(200)
    expect(removed.json.entries.wallet_storage_metadata).toBe('{"method":"passkey","version":3}')
    expect(removed.json.entries.wallet_storage_encrypted).toBeUndefined()
    expect(removed.json.entries['hd-wallet-wallets']).toBeUndefined()
  })

  test('detects wallet key replacement when the current EPM stores keys only in the EPM key vector', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-node-identity-key-vector-'))
    const { serveDesktopNodeIdentityAPI, serveDesktopNodeEPMAPI } = loadStaticServer(userData)

    const existing = await requestRaw(serveDesktopNodeEPMAPI, 'PUT', '/api/node/epm', JSON.stringify({
      dn: 'Desktop Node',
      peer_id: '12D3KooWCurrent',
      keys: [
        {
          key_type: 'signing',
          public_key: 'aa'.repeat(32)
        },
        {
          key_type: 'encryption',
          public_key: 'bb'.repeat(32)
        }
      ]
    }))
    expect(existing.statusCode).toBe(200)

    const mismatch = await requestJson(serveDesktopNodeIdentityAPI, 'PUT', '/api/node/identity/wallet', {
      wallet_identity: {
        peer_id: '12D3KooWReplacement',
        xpub: 'xpub-replacement',
        identity_public_key: '22'.repeat(33),
        signing_public_key: 'cc'.repeat(32),
        encryption_public_key: 'dd'.repeat(32),
        signature: 'ee'.repeat(64),
        signature_payload: 'payload-replacement',
        signature_timestamp: 1778700002
      }
    })
    expect(mismatch.statusCode).toBe(409)
    expect(mismatch.json.status).toBe('mismatch')
    expect(mismatch.json.current.signing_public_key).toBe('aa'.repeat(32))
    expect(mismatch.json.proposed.signing_public_key).toBe('cc'.repeat(32))
  })

  test('does not require replacement confirmation when the selected wallet has the current node peer ID', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-node-identity-same-peer-'))
    const { serveDesktopNodeIdentityAPI, serveDesktopNodeEPMAPI } = loadStaticServer(userData)

    const existing = await requestRaw(serveDesktopNodeEPMAPI, 'PUT', '/api/node/epm', JSON.stringify({
      dn: 'Desktop Node',
      peer_id: '12D3KooWSamePeer',
      signing_public_key: 'aa'.repeat(32),
      encryption_public_key: 'bb'.repeat(32)
    }))
    expect(existing.statusCode).toBe(200)

    const updated = await requestJson(serveDesktopNodeIdentityAPI, 'PUT', '/api/node/identity/wallet', {
      wallet_identity: {
        peer_id: '12D3KooWSamePeer',
        xpub: 'xpub-same-peer',
        identity_public_key: '11'.repeat(33),
        signing_public_key: 'cc'.repeat(32),
        encryption_public_key: 'dd'.repeat(32),
        signature: 'ee'.repeat(64),
        signature_payload: 'payload-same-peer',
        signature_timestamp: 1778700003
      }
    })

    expect(updated.statusCode).toBe(200)
    expect(updated.json.status).toBe('updated')
    expect(updated.json.profile.peer_id).toBe('12D3KooWSamePeer')
    expect(updated.json.profile.signing_public_key).toBe('cc'.repeat(32))
    expect(updated.json.profile.encryption_public_key).toBe('dd'.repeat(32))
  })

  test('writes wallet public keys and signatures into the local node EPM FlatBuffer', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-node-identity-epm-'))
    const { serveDesktopNodeIdentityAPI, serveDesktopNodeEPMAPI } = loadStaticServer(userData)

    await requestJson(serveDesktopNodeIdentityAPI, 'PUT', '/api/node/identity/wallet', {
      wallet_identity: {
        peer_id: '12D3KooWWalletEpm',
        wallet_account_id: 'wallet-0',
        wallet_account_label: 'Wallet 1',
        xpub: 'xpub-wallet',
        identity_public_key: '11'.repeat(33),
        signing_public_key: 'aa'.repeat(32),
        encryption_public_key: 'bb'.repeat(32),
        signature: 'cc'.repeat(64),
        signature_payload: 'payload',
        signature_timestamp: 1778700000,
        private_key: 'must-not-be-written',
        xpriv: 'must-not-be-written'
      }
    })

    const raw = await requestRaw(serveDesktopNodeEPMAPI, 'GET', '/api/node/epm')
    expect(raw.statusCode).toBe(200)

    const flatbuffers = await import('flatbuffers')
    const { EPM } = await import('spacedatastandards.org/lib/js/EPM/EPM.js')
    const { KeyType } = await import('spacedatastandards.org/lib/js/EPM/KeyType.js')
    const epm = EPM.getSizePrefixedRootAsEPM(new flatbuffers.ByteBuffer(new Uint8Array(raw.bodyBuffer)))
    expect(epm.keysLength()).toBe(2)
    expect(epm.KEYS(0).KEY_TYPE()).toBe(KeyType.Signing)
    expect(epm.KEYS(0).PUBLIC_KEY()).toBe('aa'.repeat(32))
    expect(epm.KEYS(0).PRIVATE_KEY()).toBeNull()
    expect(epm.KEYS(0).XPRIV()).toBeNull()
    expect(epm.KEYS(1).KEY_TYPE()).toBe(KeyType.Encryption)
    expect(epm.KEYS(1).PUBLIC_KEY()).toBe('bb'.repeat(32))
    expect(epm.SIGNATURE()).toBe('cc'.repeat(64))
    expect(epm.SIGNATURE_TIMESTAMP()).toBe(1778700000n)
    expect(raw.body).not.toContain('must-not-be-written')
  })

  test('publishes xpub with documented wallet signing and encryption public keys in self EPM and vCard exports', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-node-identity-xpub-'))
    const { serveDesktopIdentityAPI, serveDesktopNodeIdentityAPI, serveDesktopNodeEPMAPI } = loadStaticServer(userData)
    const xpub = HD_TEST_XPUB
    const signingPublicKey = HD_TEST_SIGNING_PUBLIC_KEY
    const encryptionPublicKey = HD_TEST_ENCRYPTION_PUBLIC_KEY

    const saved = await requestJson(serveDesktopNodeIdentityAPI, 'PUT', '/api/node/identity/wallet', {
      wallet_identity: {
        peer_id: '12D3KooWXpubDerived',
        wallet_account_id: 'wallet-0',
        wallet_account_label: 'Wallet 1',
        xpub,
        identity_public_key: '11'.repeat(33),
        signature: 'cc'.repeat(64),
        signature_payload: 'payload',
        signature_timestamp: 1778700000
      }
    })
    expect(saved.statusCode).toBe(200)
    expect(saved.json.profile).toMatchObject({
      xpub,
      signing_public_key: signingPublicKey,
      encryption_public_key: encryptionPublicKey
    })
    expect(saved.json.profile.keys).toEqual(expect.arrayContaining([
      expect.objectContaining({
        key_type: 'signing',
        public_key: signingPublicKey,
        address_type: 'secp256k1',
        xpub,
        derivation_path: "m/44'/0'/0'/0/0"
      }),
      expect.objectContaining({
        key_type: 'encryption',
        public_key: encryptionPublicKey,
        address_type: 'secp256k1',
        xpub,
        derivation_path: "m/44'/0'/0'/1/0"
      })
    ]))

    const self = await requestJson(serveDesktopIdentityAPI, 'GET', '/api/identity/epms/self')
    expect(self.statusCode).toBe(200)
    expect(self.json.epmCid).toMatch(/^bafk/)
    expect(self.json.epmJson.epm_cid).toBe(self.json.epmCid)
    expect(self.json.epmJson.signing_public_key).toBe(signingPublicKey)
    expect(self.json.epmJson.encryption_public_key).toBe(encryptionPublicKey)

    const vcard = await requestRaw(serveDesktopIdentityAPI, 'GET', '/api/identity/epms/self/vcard')
    expect(vcard.statusCode).toBe(200)
    const unfoldedVcard = vcard.body.replace(/\r\n[ \t]/g, '')
    expect(unfoldedVcard).toContain(`X-SDN-XPUB:${xpub}`)
    expect(unfoldedVcard).toContain(`X-SDN-SIGNING-PUBLIC-KEY:${signingPublicKey}`)
    expect(unfoldedVcard).toContain(`X-SDN-ENCRYPTION-PUBLIC-KEY:${encryptionPublicKey}`)
    expect(unfoldedVcard).toContain(`X-SDN-EPM-CID:${self.json.epmCid}`)
    expect(unfoldedVcard).toContain(`EMAIL;type=INTERNET;type=signing:${signingPublicKey}@signing.spacedatanetwork.org`)
    expect(unfoldedVcard).toContain(`EMAIL;type=INTERNET;type=encryption:${encryptionPublicKey}@encryption.spacedatanetwork.org`)

    const raw = await requestRaw(serveDesktopNodeEPMAPI, 'GET', '/api/node/epm')
    expect(raw.statusCode).toBe(200)
    const flatbuffers = await import('flatbuffers')
    const { EPM } = await import('spacedatastandards.org/lib/js/EPM/EPM.js')
    const { KeyType } = await import('spacedatastandards.org/lib/js/EPM/KeyType.js')
    const epm = EPM.getSizePrefixedRootAsEPM(new flatbuffers.ByteBuffer(new Uint8Array(raw.bodyBuffer)))
    expect(epm.keysLength()).toBe(2)
    expect(epm.KEYS(0).KEY_TYPE()).toBe(KeyType.Signing)
    expect(epm.KEYS(0).PUBLIC_KEY()).toBe(signingPublicKey)
    expect(epm.KEYS(0).XPUB()).toBe(xpub)
    expect(epm.KEYS(0).ADDRESS_TYPE()).toBe('secp256k1')
    expect(epm.KEYS(1).KEY_TYPE()).toBe(KeyType.Encryption)
    expect(epm.KEYS(1).PUBLIC_KEY()).toBe(encryptionPublicKey)
    expect(epm.KEYS(1).XPUB()).toBe(xpub)
    expect(epm.KEYS(1).ADDRESS_TYPE()).toBe('secp256k1')
  })

  test('serves local node EPM through desktop raw data query routes', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-local-data-api-'))
    const { serveDesktopLocalDataAPI, serveDesktopNodeEPMAPI } = loadStaticServer(userData)

    await requestRaw(serveDesktopNodeEPMAPI, 'PUT', '/api/node/epm', JSON.stringify({
      dn: 'Desktop Node',
      email: 'node@example.invalid',
      peer_id: '12D3KooWDesktopNode'
    }))

    const summary = await requestJson(serveDesktopLocalDataAPI, 'GET', '/api/v1/data/summary')
    expect(summary.statusCode).toBe(200)
    expect(summary.json.total_records).toBe(1)
    expect(summary.json.schemas).toEqual([
      expect.objectContaining({ schema_name: 'EPM.fbs', count: 1 })
    ])
    expect(summary.json.sources).toEqual([
      expect.objectContaining({ schema_name: 'EPM.fbs', provider_id: 'local-node', source_name: 'local-epm' })
    ])

    const query = await requestJson(serveDesktopLocalDataAPI, 'POST', '/api/v1/data/query', {
      schema: 'EPM.fbs',
      provider_id: 'local-node',
      source_name: 'local-epm',
      limit: 5
    })
    expect(query.statusCode).toBe(200)
    expect(query.json.records).toEqual([
      expect.objectContaining({
        schema_name: 'EPM.fbs',
        cid: expect.stringMatching(/^bafk/),
        peer_id: '12D3KooWDesktopNode',
        provider_id: 'local-node',
        source_name: 'local-epm'
      })
    ])
    const epmCid = query.json.records[0].cid
    expect(query.json.records[0].data_base64).toBeUndefined()

    const stream = await requestRaw(serveDesktopLocalDataAPI, 'POST', '/api/v1/data/query', JSON.stringify({
      schema: 'EPM.fbs',
      provider_id: 'local-node',
      source_name: 'local-epm',
      limit: 5
    }), { accept: 'application/vnd.sdn.flatbuffers.stream', 'content-type': 'application/json' })
    expect(stream.statusCode).toBe(200)
    expect(stream.headers['Content-Type']).toBe('application/vnd.sdn.flatbuffers.stream')
    expect(stream.bodyBuffer.readUInt32BE(0)).toBeGreaterThan(0)

    const record = await requestRaw(serveDesktopLocalDataAPI, 'GET', `/api/v1/data/records/EPM.fbs/${epmCid}`)
    expect(record.statusCode).toBe(200)
    expect(record.headers['Content-Type']).toBe('application/x-flatbuffers')
    expect(record.bodyBuffer.byteLength).toBeGreaterThan(0)
  })

  test('advertises configured remote data nodes with libp2p FlatSQL sync addresses only', () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-configured-sync-addrs-'))
    const configPath = path.join(userData, 'ssh-config')
    fs.writeFileSync(configPath, [
      'Host space-data-network-02 celestrak.eth',
      '    HostName 167.172.219.213',
      '    User root'
    ].join('\n'))

    const staticServer = loadStaticServer(userData)
    const nodes = staticServer.configuredSdnNodesFromSshConfig(configPath)
    const celestrak = nodes.find(node => node.id === 'space-data-network-02')

    expect(celestrak).toEqual(expect.objectContaining({
      name: 'CelesTrak Provider',
      addrs: [
        '/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3'
      ],
      metadata: expect.objectContaining({
        peer_id: '16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3',
        provider_id: 'space-data-network-02',
        epm_cid: 'bafkreiekghfegduqfol5jemuagc7rpqnvfw5ilk67d5nybhred6ubfxwr4',
        xpub: 'xpub6D36ciSsN66eJutmvXs1VXmtqnWkcMqZEbMh4FP6bpANfJpfP6oY48P7XnCWdd4NwfpHir8bU7eo3KcC45jsuN6LXwA5SYmL6sNeQwYPJjY',
        signing_public_key: '02342309cef261ec3535b5a3e7596d5a838366697bc554e68965723584184fd57c',
        signing_key_path: "m/44'/0'/0'/0/0",
        encryption_public_key: '0353b985339195a698c276925e379ba216c90dff1a9b98ec691bc466ea7176f1af',
        encryption_key_path: "m/44'/0'/0'/1/0",
        sync_protocol: '/space-data-network/flatsql-sync/1.0.0'
      })
    }))
    expect(celestrak.metadata.source_name).toBeUndefined()
    expect(celestrak.metadata.admin_proxy_path).toBeUndefined()
  })

  test('does not export a configured-node HTTP or SSH data proxy', () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-no-remote-proxy-'))
    const staticServer = loadStaticServer(userData)

    expect(staticServer.serveConfiguredSdnNodeDataProxy).toBeUndefined()
    expect(staticServer.parseConfiguredSdnNodeRemoteResponse).toBeUndefined()
  })

  test('sends cross-origin isolation headers for worker and SharedArrayBuffer support', () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-isolation-headers-'))
    const { staticAssetHeaders } = loadStaticServer(userData)

    expect(staticAssetHeaders('text/javascript; charset=utf-8')).toMatchObject({
      'Cross-Origin-Opener-Policy': 'same-origin',
      'Cross-Origin-Embedder-Policy': 'require-corp',
      'Cross-Origin-Resource-Policy': 'same-origin'
    })
  })

  test('maps observed configured SDN peer IDs to EPM display names', () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-peer-name-api-'))
    const { kuboSwarmPeersToDesktopSdnPeers } = loadStaticServer(userData)

    const peers = kuboSwarmPeersToDesktopSdnPeers({
      Peers: [
        {
          Peer: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
          Addr: '/ip4/159.203.150.8/tcp/4001',
          Identify: {
            ID: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
            AgentVersion: 'spacedatanetwork/1.0.3',
            Protocols: ['/space-data-network/module-delivery/1.0.0']
          }
        },
        {
          Peer: '16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3',
          Addr: '/ip4/167.172.219.213/tcp/4001',
          Identify: {
            ID: '16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3',
            AgentVersion: 'spacedatanetwork/1.0.3',
            Protocols: ['/space-data-network/module-delivery/1.0.0']
          }
        }
      ]
    })

    expect(peers).toEqual(expect.arrayContaining([
      expect.objectContaining({
        id: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
        name: 'SpaceAware.io',
        metadata: expect.objectContaining({
          xpub: 'xpub6DKCyLbCHZLFR4XpFg26royZdkxExSMHTjNorEgkn1kgvQbLF5sts9RfNt3PbGhphVUh7WsFQ5H6GJBh4LhmRL27oSPt1qDkJ5mAr6FZ3Wa',
          public_key: '038664c404be42123ce709e53da2b63fc24c091a968dd2200c443d7470f73fb1e6',
          signing_public_key: '038664c404be42123ce709e53da2b63fc24c091a968dd2200c443d7470f73fb1e6',
          signing_key_path: "m/44'/0'/0'/0/0",
          encryption_public_key: '0213dc855b71c36b4a7e47b034e5f0bcce8b5fdbdae95a04b3441bc8ac2db3cb41',
          encryption_key_path: "m/44'/0'/0'/1/0"
        })
      }),
      expect.objectContaining({
        id: '16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3',
        name: 'CelesTrak Provider',
        metadata: expect.objectContaining({
          xpub: 'xpub6D36ciSsN66eJutmvXs1VXmtqnWkcMqZEbMh4FP6bpANfJpfP6oY48P7XnCWdd4NwfpHir8bU7eo3KcC45jsuN6LXwA5SYmL6sNeQwYPJjY',
          epm_cid: 'bafkreiekghfegduqfol5jemuagc7rpqnvfw5ilk67d5nybhred6ubfxwr4',
          public_key: '02342309cef261ec3535b5a3e7596d5a838366697bc554e68965723584184fd57c',
          signing_public_key: '02342309cef261ec3535b5a3e7596d5a838366697bc554e68965723584184fd57c',
          signing_key_path: "m/44'/0'/0'/0/0",
          encryption_public_key: '0353b985339195a698c276925e379ba216c90dff1a9b98ec691bc466ea7176f1af',
          encryption_key_path: "m/44'/0'/0'/1/0"
        })
      })
    ]))
  })
})

// Task F1: the desktop trust vocabulary (previously 'trusted' / 'observed' /
// 'local') must map onto sdn-server's canonical PGP scale — never / unknown
// / marginal / standard / full / admin / ultimate (internal/peers.TrustLevel
// .String()) — so an xpub-registered user's trust level reads identically
// whether it was created through the desktop app or the server.
test.describe('desktop trust-level vocabulary normalization', () => {
  test('passes canonical PGP trust levels through unchanged', () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-trust-canonical-'))
    const { normalizeDesktopTrustLevel, CANONICAL_TRUST_LEVELS } = loadStaticServer(userData)

    expect(CANONICAL_TRUST_LEVELS).toEqual([
      'never', 'unknown', 'marginal', 'standard', 'full', 'admin', 'ultimate'
    ])
    for (const level of CANONICAL_TRUST_LEVELS) {
      expect(normalizeDesktopTrustLevel(level)).toBe(level)
      // Case-insensitive, as the server's own ParseTrustLevel input is.
      expect(normalizeDesktopTrustLevel(level.toUpperCase())).toBe(level)
    }
  })

  test('maps retired desktop-only vocabulary onto the canonical scale', () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-trust-legacy-desktop-'))
    const { normalizeDesktopTrustLevel } = loadStaticServer(userData)

    // The three desktop-only values named in the F1 task: SSH-configured
    // ("trusted") and swarm-discovered ("observed") peers, and the desktop
    // auth user store's former default ("local").
    expect(normalizeDesktopTrustLevel('trusted')).toBe('full')
    expect(normalizeDesktopTrustLevel('observed')).toBe('unknown')
    expect(normalizeDesktopTrustLevel('local')).toBe('admin')
  })

  test('maps server-side legacy aliases the same way peers.ParseTrustLevel does', () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-trust-legacy-server-'))
    const { normalizeDesktopTrustLevel } = loadStaticServer(userData)

    expect(normalizeDesktopTrustLevel('untrusted')).toBe('unknown')
    expect(normalizeDesktopTrustLevel('limited')).toBe('marginal')
  })

  test('falls back for unrecognized input, honoring a caller-supplied default', () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-trust-fallback-'))
    const { normalizeDesktopTrustLevel } = loadStaticServer(userData)

    expect(normalizeDesktopTrustLevel('')).toBe('unknown')
    expect(normalizeDesktopTrustLevel(undefined)).toBe('unknown')
    expect(normalizeDesktopTrustLevel('not-a-trust-level')).toBe('unknown')
    expect(normalizeDesktopTrustLevel('not-a-trust-level', 'admin')).toBe('admin')
  })

  test('the SDN peer graph reports "full" and "unknown" instead of retired desktop vocabulary', () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-trust-peer-graph-'))
    const { kuboSwarmPeersToDesktopSdnPeers } = loadStaticServer(userData)

    const peers = kuboSwarmPeersToDesktopSdnPeers({
      Peers: [{
        Peer: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
        Addr: '/ip4/159.203.150.8/tcp/4001',
        Identify: {
          ID: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
          AgentVersion: 'spacedatanetwork/1.0.3',
          Protocols: ['/space-data-network/module-delivery/1.0.0']
        }
      }]
    })

    expect(peers).toEqual([
      expect.objectContaining({ trust_level: 'unknown' })
    ])
  })
})

function loadStaticServer (userData, overrides = {}) {
  const app = {
    getPath: (name) => {
      if (name === 'userData') return userData
      return userData
    },
    getAppPath: () => path.join(__dirname, '../..')
  }
  const safeStorage = {
    isEncryptionAvailable: () => true,
    encryptString: (value) => Buffer.from(`sealed:${value}`, 'utf8'),
    decryptString: (buffer) => {
      const value = Buffer.from(buffer).toString('utf8')
      if (!value.startsWith('sealed:')) throw new Error('invalid safeStorage payload')
      return value.slice('sealed:'.length)
    }
  }
  return proxyquire('../../src/static-http-server', {
    electron: { app, safeStorage, ...overrides },
    './common/logger': {
      error: () => {},
      warn: () => {},
      info: () => {},
      debug: () => {}
    }
  })
}

async function requestJson (handler, method, url, body) {
  const result = await requestRaw(handler, method, url, body ? JSON.stringify(body) : '')
  return {
    ...result,
    json: JSON.parse(result.body)
  }
}

async function requestRaw (handler, method, url, body = '', headers = {}) {
  const req = new EventEmitter()
  req.method = method
  req.url = url
  req.headers = headers
  req.setEncoding = () => {}

  const chunks = []
  const res = {
    statusCode: 0,
    headers: {},
    writeHead (statusCode, headers = {}) {
      this.statusCode = statusCode
      this.headers = headers
    },
    end (chunk = '') {
      chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(String(chunk)))
      this.ended = true
    }
  }

  const handled = handler(req, res)
  process.nextTick(() => {
    if (body) req.emit('data', body)
    req.emit('end')
  })
  await handled

  const bodyBuffer = Buffer.concat(chunks)
  return {
    statusCode: res.statusCode,
    headers: res.headers,
    body: bodyBuffer.toString('utf8'),
    bodyBuffer
  }
}
