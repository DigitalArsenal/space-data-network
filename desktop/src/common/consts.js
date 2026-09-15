// @ts-check
const os = require('os')
const packageJson = require('../../package.json')

module.exports = Object.freeze({
  IS_MAC: os.platform() === 'darwin',
  IS_WIN: os.platform() === 'win32',
  IS_LINUX: os.platform() === 'linux',
  IS_APPIMAGE: typeof process.env.APPIMAGE !== 'undefined',
  VERSION: packageJson.version,
  ELECTRON_VERSION: process.versions.electron,
  PRODUCT_NAME: 'Space Data Network'
})
