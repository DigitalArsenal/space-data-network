const fs = require('fs')
const net = require('net')
const os = require('os')
const path = require('path')
const { test, expect } = require('@playwright/test')
const proxyquire = require('proxyquire').noCallThru()

test.describe('desktop daemon config', () => {
  test('repairs an unresponsive busy gateway port without blocking startup on a dialog', async () => {
    const sockets = new Set()
    const listener = net.createServer(socket => {
      sockets.add(socket)
      socket.on('close', () => sockets.delete(socket))
      socket.on('error', () => {})
    })
    await new Promise(resolve => listener.listen(0, '127.0.0.1', resolve))
    const gatewayPort = listener.address().port
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-daemon-config-'))
    fs.writeFileSync(path.join(userData, 'config'), JSON.stringify({
      Addresses: {
        API: '/ip4/127.0.0.1/tcp/0',
        Gateway: `/ip4/127.0.0.1/tcp/${gatewayPort}`
      }
    }, null, 2))

    const previousNodeEnv = process.env.NODE_ENV
    process.env.NODE_ENV = 'test'
    const { checkPorts } = proxyquire('../../src/daemon/config', {
      './dialogs': {
        multipleBusyPortsDialog: () => { throw new Error('blocking port dialog must not be shown') },
        busyPortDialog: () => { throw new Error('blocking port dialog must not be shown') },
        busyPortsDialog: () => { throw new Error('blocking port dialog must not be shown') }
      },
      '../common/store': {
        safeSet: () => {}
      },
      '../common/logger': {
        info: () => {},
        error: () => {}
      },
      electron: {
        shell: {
          openPath: () => Promise.resolve('')
        }
      }
    })

    try {
      const result = await Promise.race([
        checkPorts({ path: userData }),
        new Promise((_, reject) => setTimeout(() => reject(new Error('checkPorts timed out')), 1000))
      ])
      expect(result).toBe(true)
      const config = JSON.parse(fs.readFileSync(path.join(userData, 'config'), 'utf8'))
      expect(config.Addresses.Gateway).not.toContain(`/tcp/${gatewayPort}`)
    } finally {
      process.env.NODE_ENV = previousNodeEnv
      for (const socket of sockets) socket.destroy()
      await new Promise(resolve => listener.close(resolve))
    }
  })

  test('does not enable blocking busy-port dialogs unless explicitly requested', () => {
    const source = fs.readFileSync(path.join(__dirname, '../../src/daemon/config.js'), 'utf8')
    expect(source).toContain("process.env.SDN_DESKTOP_PROMPT_FOR_BUSY_PORTS === '1'")
    expect(source).toContain('using free alternative without blocking startup')
  })
})
