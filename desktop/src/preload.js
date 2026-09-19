// @ts-check
// The status page's only channel. It receives node state; it can ask to retry
// or to open the log directory. Nothing else crosses the bridge.
const { contextBridge, ipcRenderer } = require('electron')

contextBridge.exposeInMainWorld('sdn', {
  /*
   * DESKTOP, NOT A BROWSER TAB (owner 2026-09-19): the dashboard uses this to
   * drop affordances that only make sense in a browser — above all connecting
   * an EXTERNAL wallet, which needs a page-injected provider from an extension
   * that cannot exist here. The same BrowserWindow loads both the status page
   * and the node dashboard, so this reaches the dashboard too.
   *
   * A plain true, not a probe: `window.sdn` alone would also be true of any
   * future bridge, and the UI should key off the claim rather than the shape.
   */
  isDesktop: true,

  /** @param {(state: object) => void} handler */
  onState: (handler) => {
    ipcRenderer.on('node-state', (_event, state) => handler(state))
    ipcRenderer.send('node-state-request')
  },
  restart: () => ipcRenderer.send('node-restart'),
  showLogs: () => ipcRenderer.send('node-show-logs')
})
