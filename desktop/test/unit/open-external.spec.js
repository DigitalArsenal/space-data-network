const { test, expect } = require('@playwright/test')
const { EventEmitter } = require('events')
const sinon = require('sinon')
const proxyquire = require('proxyquire').noCallThru()

test.describe('open-external', () => {
  function setupModule () {
    const app = new EventEmitter()
    const shell = {
      openExternal: sinon.spy()
    }
    const launchWebUI = sinon.spy()
    const launchDashboard = sinon.spy()
    const ctx = {
      getFn: (name) => {
        if (name === 'launchWebUI') return launchWebUI
        if (name === 'launchDashboard') return launchDashboard
        throw new Error(`unexpected context function: ${name}`)
      }
    }

    const init = proxyquire('../../src/webui/open-external', {
      electron: { app, shell },
      '../context': () => ctx
    })

    init()

    const contents = new EventEmitter()
    let openHandler = null
    contents.getURL = () => ''
    contents.setWindowOpenHandler = (handler) => {
      openHandler = handler
    }
    app.emit('web-contents-created', null, contents)

    return { shell, launchWebUI, launchDashboard, contents, openHandler }
  }

  test('keeps desktop webui links inside the app and routes them to the IPFS window', async () => {
    const { launchWebUI, shell, openHandler } = setupModule()

    const result = openHandler({ url: 'webui://-/#/files' })

    expect(result).toEqual({ action: 'deny' })
    expect(launchWebUI.callCount).toBe(1)
    expect(launchWebUI.getCall(0).args[0]).toBe('/files')
    expect(shell.openExternal.callCount).toBe(0)
  })

  test('allows same-app SDN navigation without opening the system browser', async () => {
    const { launchDashboard, shell, contents } = setupModule()
    const event = { preventDefault: sinon.spy() }
    contents.getURL = () => 'webui://-/#/files'

    contents.emit('will-navigate', event, 'sdn://-/#/network')

    expect(event.preventDefault.callCount).toBe(1)
    expect(launchDashboard.callCount).toBe(1)
    expect(launchDashboard.getCall(0).args[0]).toBe('/network')
    expect(shell.openExternal.callCount).toBe(0)
  })

  test('opens true external links in the system browser', async () => {
    const { shell, contents } = setupModule()
    const event = { preventDefault: sinon.spy() }

    contents.emit('will-navigate', event, 'https://ipfs.tech/')

    expect(event.preventDefault.callCount).toBe(1)
    expect(shell.openExternal.callCount).toBe(1)
    expect(shell.openExternal.getCall(0).args[0]).toBe('https://ipfs.tech/')
  })
})
