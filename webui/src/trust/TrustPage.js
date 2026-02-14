import React, { useEffect, useMemo, useState } from 'react'
import { connect } from 'redux-bundler-react'
import Box from '../components/box/Box.js'

const TRUST_LEVELS = ['untrusted', 'limited', 'standard', 'trusted', 'admin']

const Tabs = ({ active, onChange }) => {
  const tabs = [
    { id: 'peers', label: 'Peers' },
    { id: 'groups', label: 'Groups' },
    { id: 'blocklist', label: 'Blocklist' },
    { id: 'settings', label: 'Settings' },
    { id: 'users', label: 'Users' },
    { id: 'node', label: 'Node' }
  ]
  return (
    <div className='flex flex-wrap' style={{ gap: 8, marginBottom: 16 }}>
      {tabs.map(t => (
        <button
          key={t.id}
          className='pointer'
          onClick={() => onChange(t.id)}
          style={{
            padding: '8px 10px',
            borderRadius: 10,
            border: `1px solid ${active === t.id ? 'rgba(88, 166, 255, 0.7)' : 'var(--sdn-border)'}`,
            background: active === t.id ? 'rgba(88, 166, 255, 0.10)' : 'var(--sdn-bg-tertiary)',
            color: 'var(--sdn-text-primary)',
            fontWeight: 700,
            fontFamily: 'Montserrat, sans-serif',
            letterSpacing: '0.02em'
          }}
        >
          {t.label}
        </button>
      ))}
    </div>
  )
}

export const TrustPage = ({
  isAdminUser: isAdmin,
  trustLoading,
  trustError,
  trustedPeers,
  peerGroups,
  blockedPeers,
  trustSettings,
  authUsers,
  nodeInfo,
  doFetchTrustData,
  doAddTrustedPeer,
  doUpdateTrustedPeer,
  doRemoveTrustedPeer,
  doAddGroup,
  doRemoveGroup,
  doBlockPeer,
  doUnblockPeer,
  doSetStrictMode,
  doAddAuthUser,
  doRemoveAuthUser,
  doUpdateAuthUserTrust,
  doUpdateHash
}) => {
  const [tab, setTab] = useState('peers')
  const [opError, setOpError] = useState(null)

  const peersSorted = useMemo(() => {
    const peers = Array.isArray(trustedPeers) ? [...trustedPeers] : []
    peers.sort((a, b) => String(a?.trust_level || '').localeCompare(String(b?.trust_level || '')) ||
      String(a?.name || '').localeCompare(String(b?.name || '')) ||
      String(a?.id || '').localeCompare(String(b?.id || '')))
    return peers
  }, [trustedPeers])

  // Forms
  const [newPeer, setNewPeer] = useState({
    id: '',
    trust_level: 'standard',
    name: '',
    organization: '',
    addrs: '',
    groups: '',
    notes: ''
  })
  const [newGroup, setNewGroup] = useState({
    name: '',
    description: '',
    default_trust_level: 'standard'
  })
  const [blockPeerId, setBlockPeerId] = useState('')
  const [newUser, setNewUser] = useState({
    xpub: '',
    name: '',
    trust_level: 'standard',
    signing_pubkey_hex: ''
  })

  useEffect(() => {
    if (!isAdmin) {
      doUpdateHash('/status')
      return
    }
    doFetchTrustData()
  }, [isAdmin, doFetchTrustData, doUpdateHash])

  if (!isAdmin) {
    return (
      <Box>
        <h1 className='f3 ma0 mb2' style={{ color: 'var(--sdn-text-primary)' }}>Trust</h1>
        <p className='ma0' style={{ color: 'var(--sdn-text-secondary)' }}>
          Admin access is required.
        </p>
      </Box>
    )
  }

  const onOp = async (fn) => {
    setOpError(null)
    try {
      await fn()
    } catch (e) {
      setOpError(e && e.message ? e.message : 'Operation failed')
    }
  }

  return (
    <Box>
      <h1 className='f3 ma0 mb2' style={{ color: 'var(--sdn-text-primary)' }}>Trust</h1>
      <p className='ma0 mb3' style={{ color: 'var(--sdn-text-secondary)' }}>
        Manage peer trust, groups, blocklist, wallet-auth users, and node identity.
      </p>

      <Tabs active={tab} onChange={setTab} />

      {trustLoading && <div className='mb3' style={{ color: 'var(--sdn-text-secondary)' }}>Loading…</div>}
      {trustError && <div className='mb3' style={{ color: 'var(--sdn-accent-red)' }}>{trustError}</div>}
      {opError && <div className='mb3' style={{ color: 'var(--sdn-accent-red)' }}>{opError}</div>}

      {tab === 'peers' && (
        <div>
          <h2 className='f4 mt0 mb2' style={{ color: 'var(--sdn-text-primary)' }}>Trusted Peers</h2>

          <div className='mb3' style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
            <input
              className='input-reset pa2 ba br2'
              placeholder='Peer ID'
              value={newPeer.id}
              onChange={(e) => setNewPeer({ ...newPeer, id: e.target.value })}
            />
            <select
              className='input-reset pa2 ba br2'
              value={newPeer.trust_level}
              onChange={(e) => setNewPeer({ ...newPeer, trust_level: e.target.value })}
            >
              {TRUST_LEVELS.map(l => <option key={l} value={l}>{l}</option>)}
            </select>
            <input
              className='input-reset pa2 ba br2'
              placeholder='Name (optional)'
              value={newPeer.name}
              onChange={(e) => setNewPeer({ ...newPeer, name: e.target.value })}
            />
            <input
              className='input-reset pa2 ba br2'
              placeholder='Organization (optional)'
              value={newPeer.organization}
              onChange={(e) => setNewPeer({ ...newPeer, organization: e.target.value })}
            />
            <input
              className='input-reset pa2 ba br2'
              placeholder='Multiaddrs (comma-separated)'
              value={newPeer.addrs}
              onChange={(e) => setNewPeer({ ...newPeer, addrs: e.target.value })}
            />
            <input
              className='input-reset pa2 ba br2'
              placeholder='Groups (comma-separated)'
              value={newPeer.groups}
              onChange={(e) => setNewPeer({ ...newPeer, groups: e.target.value })}
            />
            <input
              className='input-reset pa2 ba br2'
              placeholder='Notes (optional)'
              value={newPeer.notes}
              onChange={(e) => setNewPeer({ ...newPeer, notes: e.target.value })}
              style={{ gridColumn: '1 / -1' }}
            />
            <div style={{ gridColumn: '1 / -1' }}>
              <button
                className='pointer'
                onClick={() => onOp(async () => {
                  const addrs = newPeer.addrs.split(',').map(s => s.trim()).filter(Boolean)
                  const groups = newPeer.groups.split(',').map(s => s.trim()).filter(Boolean)
                  await doAddTrustedPeer({
                    id: newPeer.id.trim(),
                    trust_level: newPeer.trust_level,
                    name: newPeer.name.trim(),
                    organization: newPeer.organization.trim(),
                    addrs,
                    groups,
                    notes: newPeer.notes
                  })
                  setNewPeer({ id: '', trust_level: 'standard', name: '', organization: '', addrs: '', groups: '', notes: '' })
                })}
                style={{
                  padding: '9px 12px',
                  borderRadius: 10,
                  border: '1px solid rgba(88, 166, 255, 0.6)',
                  background: 'var(--sdn-bg-tertiary)',
                  color: 'var(--sdn-text-primary)',
                  fontWeight: 700,
                  fontFamily: 'Montserrat, sans-serif'
                }}
              >
                Add Peer
              </button>
            </div>
          </div>

          <div className='overflow-auto'>
            <table className='w-100 collapse'>
              <thead>
                <tr style={{ background: 'var(--sdn-table-header)' }}>
                  <th className='tl pa2' style={{ color: 'var(--sdn-text-secondary)' }}>Peer ID</th>
                  <th className='tl pa2' style={{ color: 'var(--sdn-text-secondary)' }}>Trust</th>
                  <th className='tl pa2' style={{ color: 'var(--sdn-text-secondary)' }}>Name</th>
                  <th className='tl pa2' style={{ color: 'var(--sdn-text-secondary)' }}>Org</th>
                  <th className='tl pa2' style={{ color: 'var(--sdn-text-secondary)' }}>Groups</th>
                  <th className='tl pa2' style={{ color: 'var(--sdn-text-secondary)' }} />
                </tr>
              </thead>
              <tbody>
                {peersSorted.map(p => (
                  <tr key={p.id} style={{ borderTop: '1px solid var(--sdn-border)' }}>
                    <td className='pa2' style={{ fontFamily: 'SFMono-Regular,ui-monospace,Menlo,Monaco,Consolas,monospace', color: 'var(--sdn-accent)' }}>
                      {p.id}
                    </td>
                    <td className='pa2'>
                      <select
                        className='input-reset pa2 ba br2'
                        value={p.trust_level || 'standard'}
                        onChange={(e) => onOp(() => doUpdateTrustedPeer(p.id, { trust_level: e.target.value }))}
                      >
                        {TRUST_LEVELS.map(l => <option key={l} value={l}>{l}</option>)}
                      </select>
                    </td>
                    <td className='pa2' style={{ color: 'var(--sdn-text-primary)' }}>{p.name || ''}</td>
                    <td className='pa2' style={{ color: 'var(--sdn-text-primary)' }}>{p.organization || ''}</td>
                    <td className='pa2' style={{ color: 'var(--sdn-text-primary)' }}>{Array.isArray(p.groups) ? p.groups.join(', ') : ''}</td>
                    <td className='pa2 tr'>
                      <button
                        className='pointer'
                        onClick={() => onOp(() => doRemoveTrustedPeer(p.id))}
                        style={{
                          padding: '7px 10px',
                          borderRadius: 10,
                          border: '1px solid rgba(248, 81, 73, 0.6)',
                          background: 'rgba(248, 81, 73, 0.10)',
                          color: 'var(--sdn-text-primary)',
                          fontWeight: 700
                        }}
                      >
                        Remove
                      </button>
                    </td>
                  </tr>
                ))}
                {peersSorted.length === 0 && (
                  <tr>
                    <td className='pa3' colSpan={6} style={{ color: 'var(--sdn-text-secondary)' }}>No trusted peers.</td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {tab === 'groups' && (
        <div>
          <h2 className='f4 mt0 mb2' style={{ color: 'var(--sdn-text-primary)' }}>Peer Groups</h2>

          <div className='mb3' style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
            <input
              className='input-reset pa2 ba br2'
              placeholder='Group name'
              value={newGroup.name}
              onChange={(e) => setNewGroup({ ...newGroup, name: e.target.value })}
            />
            <select
              className='input-reset pa2 ba br2'
              value={newGroup.default_trust_level}
              onChange={(e) => setNewGroup({ ...newGroup, default_trust_level: e.target.value })}
            >
              {TRUST_LEVELS.map(l => <option key={l} value={l}>{l}</option>)}
            </select>
            <input
              className='input-reset pa2 ba br2'
              placeholder='Description (optional)'
              value={newGroup.description}
              onChange={(e) => setNewGroup({ ...newGroup, description: e.target.value })}
              style={{ gridColumn: '1 / -1' }}
            />
            <div style={{ gridColumn: '1 / -1' }}>
              <button
                className='pointer'
                onClick={() => onOp(async () => {
                  await doAddGroup({
                    name: newGroup.name.trim(),
                    description: newGroup.description,
                    default_trust_level: newGroup.default_trust_level
                  })
                  setNewGroup({ name: '', description: '', default_trust_level: 'standard' })
                })}
                style={{
                  padding: '9px 12px',
                  borderRadius: 10,
                  border: '1px solid rgba(88, 166, 255, 0.6)',
                  background: 'var(--sdn-bg-tertiary)',
                  color: 'var(--sdn-text-primary)',
                  fontWeight: 700,
                  fontFamily: 'Montserrat, sans-serif'
                }}
              >
                Create Group
              </button>
            </div>
          </div>

          <div className='overflow-auto'>
            <table className='w-100 collapse'>
              <thead>
                <tr style={{ background: 'var(--sdn-table-header)' }}>
                  <th className='tl pa2' style={{ color: 'var(--sdn-text-secondary)' }}>Name</th>
                  <th className='tl pa2' style={{ color: 'var(--sdn-text-secondary)' }}>Default Trust</th>
                  <th className='tl pa2' style={{ color: 'var(--sdn-text-secondary)' }}>Description</th>
                  <th className='tl pa2' style={{ color: 'var(--sdn-text-secondary)' }} />
                </tr>
              </thead>
              <tbody>
                {(peerGroups || []).map(g => (
                  <tr key={g.name} style={{ borderTop: '1px solid var(--sdn-border)' }}>
                    <td className='pa2' style={{ color: 'var(--sdn-text-primary)', fontWeight: 700 }}>{g.name}</td>
                    <td className='pa2' style={{ color: 'var(--sdn-text-primary)' }}>{g.default_trust_level}</td>
                    <td className='pa2' style={{ color: 'var(--sdn-text-primary)' }}>{g.description || ''}</td>
                    <td className='pa2 tr'>
                      <button
                        className='pointer'
                        onClick={() => onOp(() => doRemoveGroup(g.name))}
                        style={{
                          padding: '7px 10px',
                          borderRadius: 10,
                          border: '1px solid rgba(248, 81, 73, 0.6)',
                          background: 'rgba(248, 81, 73, 0.10)',
                          color: 'var(--sdn-text-primary)',
                          fontWeight: 700
                        }}
                      >
                        Remove
                      </button>
                    </td>
                  </tr>
                ))}
                {(peerGroups || []).length === 0 && (
                  <tr>
                    <td className='pa3' colSpan={4} style={{ color: 'var(--sdn-text-secondary)' }}>No groups.</td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {tab === 'blocklist' && (
        <div>
          <h2 className='f4 mt0 mb2' style={{ color: 'var(--sdn-text-primary)' }}>Blocklist</h2>

          <div className='mb3' style={{ display: 'flex', gap: 10, alignItems: 'center' }}>
            <input
              className='input-reset pa2 ba br2'
              placeholder='Peer ID'
              value={blockPeerId}
              onChange={(e) => setBlockPeerId(e.target.value)}
              style={{ flex: 1 }}
            />
            <button
              className='pointer'
              onClick={() => onOp(async () => {
                await doBlockPeer(blockPeerId.trim())
                setBlockPeerId('')
              })}
              style={{
                padding: '9px 12px',
                borderRadius: 10,
                border: '1px solid rgba(248, 81, 73, 0.6)',
                background: 'rgba(248, 81, 73, 0.10)',
                color: 'var(--sdn-text-primary)',
                fontWeight: 700
              }}
            >
              Block
            </button>
          </div>

          <ul className='list pl0 ma0'>
            {(blockedPeers || []).map(pid => (
              <li key={pid} className='flex items-center justify-between pa2' style={{ borderTop: '1px solid var(--sdn-border)' }}>
                <span style={{ fontFamily: 'SFMono-Regular,ui-monospace,Menlo,Monaco,Consolas,monospace', color: 'var(--sdn-accent)' }}>{pid}</span>
                <button
                  className='pointer'
                  onClick={() => onOp(() => doUnblockPeer(pid))}
                  style={{
                    padding: '7px 10px',
                    borderRadius: 10,
                    border: '1px solid rgba(88, 166, 255, 0.6)',
                    background: 'var(--sdn-bg-tertiary)',
                    color: 'var(--sdn-text-primary)',
                    fontWeight: 700
                  }}
                >
                  Unblock
                </button>
              </li>
            ))}
            {(blockedPeers || []).length === 0 && (
              <li className='pa2' style={{ color: 'var(--sdn-text-secondary)' }}>No blocked peers.</li>
            )}
          </ul>
        </div>
      )}

      {tab === 'settings' && (
        <div>
          <h2 className='f4 mt0 mb2' style={{ color: 'var(--sdn-text-primary)' }}>Trust Settings</h2>
          <div className='pa3' style={{ border: '1px solid var(--sdn-border)', borderRadius: 12, background: 'var(--sdn-bg-tertiary)' }}>
            <div className='flex items-center justify-between'>
              <div>
                <div style={{ fontWeight: 800, color: 'var(--sdn-text-primary)' }}>Strict Mode</div>
                <div style={{ fontSize: 13, color: 'var(--sdn-text-secondary)', marginTop: 4 }}>
                  When enabled, only peers in the trusted registry are allowed to connect.
                </div>
              </div>
              <button
                className='pointer'
                onClick={() => onOp(() => doSetStrictMode(!(trustSettings && trustSettings.strict_mode)))}
                style={{
                  padding: '9px 12px',
                  borderRadius: 10,
                  border: `1px solid ${trustSettings && trustSettings.strict_mode ? 'rgba(63,185,80,0.7)' : 'var(--sdn-border)'}`,
                  background: trustSettings && trustSettings.strict_mode ? 'rgba(63,185,80,0.10)' : 'var(--sdn-bg-secondary)',
                  color: 'var(--sdn-text-primary)',
                  fontWeight: 800
                }}
              >
                {trustSettings && trustSettings.strict_mode ? 'Enabled' : 'Disabled'}
              </button>
            </div>
          </div>
        </div>
      )}

      {tab === 'users' && (
        <div>
          <h2 className='f4 mt0 mb2' style={{ color: 'var(--sdn-text-primary)' }}>Wallet-Auth Users</h2>

          <div className='mb3' style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
            <input
              className='input-reset pa2 ba br2'
              placeholder='xpub'
              value={newUser.xpub}
              onChange={(e) => setNewUser({ ...newUser, xpub: e.target.value })}
              style={{ gridColumn: '1 / -1' }}
            />
            <input
              className='input-reset pa2 ba br2'
              placeholder='Name (optional)'
              value={newUser.name}
              onChange={(e) => setNewUser({ ...newUser, name: e.target.value })}
            />
            <select
              className='input-reset pa2 ba br2'
              value={newUser.trust_level}
              onChange={(e) => setNewUser({ ...newUser, trust_level: e.target.value })}
            >
              {TRUST_LEVELS.map(l => <option key={l} value={l}>{l}</option>)}
            </select>
            <input
              className='input-reset pa2 ba br2'
              placeholder='Signing pubkey hex (Ed25519)'
              value={newUser.signing_pubkey_hex}
              onChange={(e) => setNewUser({ ...newUser, signing_pubkey_hex: e.target.value })}
              style={{ gridColumn: '1 / -1' }}
            />
            <div style={{ gridColumn: '1 / -1' }}>
              <button
                className='pointer'
                onClick={() => onOp(async () => {
                  await doAddAuthUser({
                    xpub: newUser.xpub.trim(),
                    name: newUser.name.trim(),
                    trust_level: newUser.trust_level,
                    signing_pubkey_hex: newUser.signing_pubkey_hex.trim()
                  })
                  setNewUser({ xpub: '', name: '', trust_level: 'standard', signing_pubkey_hex: '' })
                })}
                style={{
                  padding: '9px 12px',
                  borderRadius: 10,
                  border: '1px solid rgba(88, 166, 255, 0.6)',
                  background: 'var(--sdn-bg-tertiary)',
                  color: 'var(--sdn-text-primary)',
                  fontWeight: 700,
                  fontFamily: 'Montserrat, sans-serif'
                }}
              >
                Add User
              </button>
            </div>
          </div>

          <div className='overflow-auto'>
            <table className='w-100 collapse'>
              <thead>
                <tr style={{ background: 'var(--sdn-table-header)' }}>
                  <th className='tl pa2' style={{ color: 'var(--sdn-text-secondary)' }}>Name</th>
                  <th className='tl pa2' style={{ color: 'var(--sdn-text-secondary)' }}>Trust</th>
                  <th className='tl pa2' style={{ color: 'var(--sdn-text-secondary)' }}>xpub</th>
                  <th className='tl pa2' style={{ color: 'var(--sdn-text-secondary)' }}>Signing Key</th>
                  <th className='tl pa2' style={{ color: 'var(--sdn-text-secondary)' }}>Source</th>
                  <th className='tl pa2' style={{ color: 'var(--sdn-text-secondary)' }} />
                </tr>
              </thead>
              <tbody>
                {(authUsers || []).map(u => (
                  <tr key={u.xpub} style={{ borderTop: '1px solid var(--sdn-border)' }}>
                    <td className='pa2' style={{ color: 'var(--sdn-text-primary)', fontWeight: 700 }}>{u.name || ''}</td>
                    <td className='pa2'>
                      <select
                        className='input-reset pa2 ba br2'
                        value={u.trust_level || 'standard'}
                        onChange={(e) => onOp(() => doUpdateAuthUserTrust(u.xpub, e.target.value))}
                      >
                        {TRUST_LEVELS.map(l => <option key={l} value={l}>{l}</option>)}
                      </select>
                    </td>
                    <td className='pa2' style={{ fontFamily: 'SFMono-Regular,ui-monospace,Menlo,Monaco,Consolas,monospace', color: 'var(--sdn-accent)' }}>
                      {u.xpub}
                    </td>
                    <td className='pa2' style={{ fontFamily: 'SFMono-Regular,ui-monospace,Menlo,Monaco,Consolas,monospace', color: u.signing_pubkey_hex ? 'var(--sdn-text-primary)' : 'var(--sdn-accent-red)' }}>
                      {u.signing_pubkey_hex
                        ? (String(u.signing_pubkey_hex).length > 18 ? `${String(u.signing_pubkey_hex).slice(0, 10)}...${String(u.signing_pubkey_hex).slice(-6)}` : String(u.signing_pubkey_hex))
                        : 'missing'}
                    </td>
                    <td className='pa2' style={{ color: 'var(--sdn-text-primary)' }}>{u.source || ''}</td>
                    <td className='pa2 tr'>
                      <button
                        className='pointer'
                        onClick={() => onOp(() => doRemoveAuthUser(u.xpub))}
                        style={{
                          padding: '7px 10px',
                          borderRadius: 10,
                          border: '1px solid rgba(248, 81, 73, 0.6)',
                          background: 'rgba(248, 81, 73, 0.10)',
                          color: 'var(--sdn-text-primary)',
                          fontWeight: 700
                        }}
                      >
                        Remove
                      </button>
                    </td>
                  </tr>
                ))}
                {(authUsers || []).length === 0 && (
                  <tr>
                    <td className='pa3' colSpan={6} style={{ color: 'var(--sdn-text-secondary)' }}>No users.</td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {tab === 'node' && (
        <div>
          <h2 className='f4 mt0 mb2' style={{ color: 'var(--sdn-text-primary)' }}>Node Identity</h2>
          <div className='pa3' style={{ border: '1px solid var(--sdn-border)', borderRadius: 12, background: 'var(--sdn-bg-tertiary)' }}>
            {!nodeInfo && <div style={{ color: 'var(--sdn-text-secondary)' }}>No node info.</div>}
            {nodeInfo && (
              <div style={{ display: 'grid', gridTemplateColumns: '160px 1fr', gap: 10 }}>
                <div style={{ color: 'var(--sdn-text-secondary)' }}>Peer ID</div>
                <div style={{ color: 'var(--sdn-accent)', fontFamily: 'SFMono-Regular,ui-monospace,Menlo,Monaco,Consolas,monospace' }}>{nodeInfo.peer_id}</div>
                <div style={{ color: 'var(--sdn-text-secondary)' }}>Mode</div>
                <div style={{ color: 'var(--sdn-text-primary)' }}>{nodeInfo.mode}</div>
                <div style={{ color: 'var(--sdn-text-secondary)' }}>Version</div>
                <div style={{ color: 'var(--sdn-text-primary)' }}>{nodeInfo.version}</div>
                <div style={{ color: 'var(--sdn-text-secondary)' }}>Listen</div>
                <div style={{ color: 'var(--sdn-text-primary)', fontFamily: 'SFMono-Regular,ui-monospace,Menlo,Monaco,Consolas,monospace', fontSize: 12 }}>
                  {(nodeInfo.listen_addresses || []).join('\n')}
                </div>
              </div>
            )}
          </div>
        </div>
      )}
    </Box>
  )
}

export default connect(
  'selectIsAdminUser',
  'selectTrustLoading',
  'selectTrustError',
  'selectTrustedPeers',
  'selectPeerGroups',
  'selectBlockedPeers',
  'selectTrustSettings',
  'selectAuthUsers',
  'selectTrustNodeInfo',
  'doFetchTrustData',
  'doAddTrustedPeer',
  'doUpdateTrustedPeer',
  'doRemoveTrustedPeer',
  'doAddGroup',
  'doRemoveGroup',
  'doBlockPeer',
  'doUnblockPeer',
  'doSetStrictMode',
  'doAddAuthUser',
  'doRemoveAuthUser',
  'doUpdateAuthUserTrust',
  'doUpdateHash',
  TrustPage
)
