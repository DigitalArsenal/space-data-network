// @ts-check
// The status page's only channel. It receives node state; it can ask to retry
// or to open the log directory. Nothing else crosses the bridge.
const { contextBridge, ipcRenderer } = require('electron')

contextBridge.exposeInMainWorld('sdn', {
  /** @param {(state: object) => void} handler */
  onState: (handler) => {
    ipcRenderer.on('node-state', (_event, state) => handler(state))
    ipcRenderer.send('node-state-request')
  },
  restart: () => ipcRenderer.send('node-restart'),
  showLogs: () => ipcRenderer.send('node-show-logs')
})
