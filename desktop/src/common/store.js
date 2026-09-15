// @ts-check
const Store = require('electron-store')
const CONFIG_KEYS = require('./config-keys')

/**
 * The shell's own preferences. The node's configuration is NOT mirrored here:
 * the config file the node reads is the single source of truth, so an operator
 * who edits it by hand is never overruled by a stale copy in this store.
 */
const store = new Store({
  defaults: {
    [CONFIG_KEYS.AUTO_LAUNCH]: false
  }
})

module.exports = store
