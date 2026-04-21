const { contextBridge } = require('electron')

contextBridge.exposeInMainWorld('__SDN_CONFIG__', {
  ipfsDashboardUrl: 'webui://-/#/',
})
