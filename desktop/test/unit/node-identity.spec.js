const { test, expect } = require('@playwright/test')
const proxyquire = require('proxyquire').noCallThru()

const mockElectron = require('./mocks/electron')

const { parseIdentityReport } = proxyquire('../../src/node/identity', {
  electron: mockElectron(),
  '../common/logger': { info () {}, warn () {}, error () {}, debug () {}, logsPath: '' },
  '../dialogs': { showDialog () {}, errorDialog () {} }
})

const REPORT = [
  'config: /u/node/config.yaml (from --config)',
  '',
  '--- SDN Node Identity ---',
  'PeerID:         12D3KooWExample',
  'XPub:           xpub661MyExample',
  'Mnemonic File:  /u/node/keys/mnemonic',
  '',
  '*** MNEMONIC (SENSITIVE — DO NOT SHARE) ***',
  'abandon ability able about above absent absorb abstract absurd abuse access accident',
  ''
].join('\n')

test.describe('the recovery phrase the node prints', () => {
  test('separates the phrase from the identity summary', () => {
    const { summary, phrase } = parseIdentityReport(REPORT)

    expect(phrase).toBe('abandon ability able about above absent absorb abstract absurd abuse access accident')
    expect(summary).toContain('PeerID:         12D3KooWExample')
    expect(summary).not.toContain('abandon')
    expect(summary).not.toContain('config:')
  })

  test('returns no phrase when the node did not print one', () => {
    const withoutMnemonic = REPORT.split('*** MNEMONIC')[0]
    const { phrase, summary } = parseIdentityReport(withoutMnemonic)

    expect(phrase).toBe('')
    expect(summary).toContain('XPub:           xpub661MyExample')
  })
})
