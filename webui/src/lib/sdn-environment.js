/**
 * SDN Environment Detection - Phase 17.4: Shared UI for Desktop & Server Daemon
 *
 * Detects whether the UI is running inside Electron (desktop app) or
 * a standard browser (served by sdn-server HTTP daemon).
 *
 * Single build serves both modes. Components can use these utilities
 * to adapt their behavior.
 */

/**
 * Check if running inside Electron
 * @returns {boolean}
 */
export function isElectron () {
  // Renderer process detection
  if (typeof window !== 'undefined' && typeof window.process === 'object' &&
      window.process.type === 'renderer') {
    return true
  }

  // Main process detection
  if (typeof process !== 'undefined' && typeof process.versions === 'object' &&
      !!process.versions.electron) {
    return true
  }

  // User agent detection (fallback)
  if (typeof navigator === 'object' && typeof navigator.userAgent === 'string' &&
      navigator.userAgent.indexOf('Electron') >= 0) {
    return true
  }

  // Check for ipfs-desktop bridge
  if (typeof window !== 'undefined' && window.ipfsDesktop) {
    return true
  }

  return false
}

/**
 * Check if running in a standard browser (served by HTTP daemon)
 * @returns {boolean}
 */
export function isBrowser () {
  return !isElectron()
}

/**
 * Get the environment mode string
 * @returns {'electron' | 'browser'}
 */
export function getEnvironment () {
  return isElectron() ? 'electron' : 'browser'
}

/**
 * Get the appropriate IPFS API URL based on environment
 *
 * In Electron: uses the locally running IPFS node
 * In Browser:  uses the configured API endpoint (may be remote sdn-server)
 *
 * @returns {string}
 */
export function getApiUrl () {
  if (isElectron()) {
    // Electron runs its own local IPFS node
    return 'http://127.0.0.1:5001'
  }

  // Browser mode: check for configured endpoint or default
  if (typeof window !== 'undefined' && window.location) {
    // If served from sdn-server, the API is on the same host
    const { protocol, hostname } = window.location
    return `${protocol}//${hostname}:5001`
  }

  return 'http://127.0.0.1:5001'
}

/**
 * Feature flags based on environment
 * @returns {Object}
 */
export function getEnvironmentFeatures () {
  const electron = isElectron()
  return {
    // Desktop-only features
    canManageNode: electron,
    hasSystemTray: electron,
    canAutoStart: electron,
    hasNativeMenus: electron,

    // Available in both modes
    canBrowseFiles: true,
    canViewPeers: true,
    canViewStatus: true,
    canEditSettings: true,

    // Browser-only features (served by sdn-server)
    showApiAddressForm: !electron,
    showConnectionBanner: !electron
  }
}
