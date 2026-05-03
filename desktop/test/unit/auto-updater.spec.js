const { EventEmitter } = require('events')
const { test, expect } = require('@playwright/test')
const proxyquire = require('proxyquire').noCallThru()
const sinon = require('sinon')

function loadAutoUpdater ({ appName = 'Space Data Network', storeValue = false } = {}) {
  const logger = {
    error: sinon.spy(),
    info: sinon.spy()
  }
  const shell = {
    openExternal: sinon.spy()
  }
  const ctx = {
    setProp: sinon.spy(),
    getFn: () => sinon.spy()
  }
  const electronAutoUpdater = new EventEmitter()
  const electronUpdater = {
    autoUpdater: Object.assign(new EventEmitter(), {
      checkForUpdates: sinon.spy(),
      downloadUpdate: sinon.spy()
    })
  }

  const module = proxyquire('../../src/auto-updater', {
    electron: {
      app: {
        getName: () => appName
      },
      autoUpdater: electronAutoUpdater,
      BrowserWindow: {
        getAllWindows: () => []
      },
      ipcMain: new EventEmitter(),
      Notification: class {},
      shell
    },
    'electron-updater': electronUpdater,
    i18next: {
      t: (key) => key
    },
    '../common/logger': logger,
    '../dialogs': {
      showDialog: sinon.stub().returns(0)
    },
    '../common/consts': {
      IS_APPIMAGE: false,
      IS_MAC: true,
      IS_WIN: false
    },
    '../context': () => ctx,
    '../common/store': {
      get: sinon.stub().returns(storeValue)
    }
  })

  return { ctx, logger, module, shell, updater: electronUpdater.autoUpdater }
}

test.describe('auto updater', () => {
  test('disables upstream IPFS Desktop updates for Space Data Network builds', () => {
    const { logger, module } = loadAutoUpdater()

    expect(module._isSpaceDataNetworkBuild()).toBe(true)
    expect(module._isAutoUpdateSupported()).toBe(false)
    expect(logger.info.calledWith('[updater] upstream IPFS auto update disabled for Space Data Network builds')).toBe(true)
  })

  test('keeps manual update checks pointed at SDN releases when auto update is disabled', async () => {
    const previousNodeEnv = process.env.NODE_ENV
    process.env.NODE_ENV = 'production'
    const { ctx, module, shell, updater } = loadAutoUpdater()

    try {
      await module()
    } finally {
      process.env.NODE_ENV = previousNodeEnv
    }

    expect(ctx.setProp.callCount).toBe(1)
    expect(ctx.setProp.getCall(0).args[0]).toBe('manualCheckForUpdates')
    ctx.setProp.getCall(0).args[1]()
    expect(shell.openExternal.calledWith(module.SDN_RELEASES_URL)).toBe(true)
    expect(updater.checkForUpdates.callCount).toBe(0)
  })
})
