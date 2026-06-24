const fs = require('fs')
const path = require('path')
const { test, expect } = require('@playwright/test')

test.describe('SDN tray service lifecycle controls', () => {
  test('wires start, stop, and restart controls to daemon lifecycle functions', () => {
    const source = fs.readFileSync(path.join(__dirname, '../../src/tray.js'), 'utf8')

    for (const fnName of ['restartIpfs', 'startIpfs', 'stopIpfs']) {
      expect(source).toContain(`const ${fnName} = ctx.getFn('${fnName}')`)
      expect(source).toContain(`click: () => { ${fnName}() }`)
    }

    expect(source).toContain("id: 'restartIpfs'")
    expect(source).toContain("id: 'startIpfs'")
    expect(source).toContain("id: 'stopIpfs'")
    expect(source).toContain("label: i18n.t('restart')")
    expect(source).toContain("label: i18n.t('start')")
    expect(source).toContain("label: i18n.t('stop')")
  })

  test('keeps lifecycle controls visible only in the matching daemon state', () => {
    const source = fs.readFileSync(path.join(__dirname, '../../src/tray.js'), 'utf8')

    expect(source).toContain("menu.getMenuItemById('startIpfs').visible = status === STATUS.STOPPING_FINISHED")
    expect(source).toContain("menu.getMenuItemById('stopIpfs').visible = status === STATUS.STARTING_FINISHED")
    expect(source).toContain("menu.getMenuItemById('restartIpfs').visible = (status === STATUS.STARTING_FINISHED || errored)")
  })

  test('disables lifecycle controls while garbage collection owns the daemon', () => {
    const source = fs.readFileSync(path.join(__dirname, '../../src/tray.js'), 'utf8')

    expect(source).toContain("menu.getMenuItemById('startIpfs').enabled = !gcRunning")
    expect(source).toContain("menu.getMenuItemById('stopIpfs').enabled = !gcRunning")
    expect(source).toContain("menu.getMenuItemById('restartIpfs').enabled = !gcRunning")
  })
})
