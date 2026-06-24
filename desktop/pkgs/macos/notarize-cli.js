require('dotenv').config()
const { notarize } = require('electron-notarize-dmg')

// Manual online notarization (no stapling) via CLI
// ================================================
// Note: this assumes APPLE_ID, APPLE_APP_SPECIFIC_PASSWORD, and APPLE_TEAM_ID
// are set as env variables or in .env.
//
// Usage:
// 1. Define APPLE_ID, APPLE_APP_SPECIFIC_PASSWORD, and APPLE_TEAM_ID
// 2. node ./notarize.js path/to/Space-Data-Network.dmg
//
// Note on stapling and this script:
// We disable stapling of the dmg file, as it changes its contents.  It
// would break auto update files.  It is perfectly okay to notarize and not
// staple to keep the file intact. This requires end users to have connectivity
// to validate the file, but they had it to get .dmg in the first place.

;(async () => {
  const artifactPath = process.argv[2]
  if (!artifactPath || !artifactPath.endsWith('.dmg')) {
    console.log('Missing artifact path: pass .dmg file as CLI argument')
    process.exit(1)
  }
  if (!process.env.APPLE_ID || !process.env.APPLE_APP_SPECIFIC_PASSWORD || !process.env.APPLE_TEAM_ID) {
    console.log('Define APPLE_ID, APPLE_APP_SPECIFIC_PASSWORD, and APPLE_TEAM_ID as env variables or in .env file')
    process.exit(1)
  }
  console.log(`Initializing notarization of DMG at ${artifactPath}`)
  await notarize({
    appBundleId: 'org.spacedatanetwork.desktop',
    dmgPath: artifactPath,
    staple: false,
    appleId: process.env.APPLE_ID,
    appleIdPassword: process.env.APPLE_APP_SPECIFIC_PASSWORD,
    tool: 'notarytool',
    teamId: process.env.APPLE_TEAM_ID
  })
})()
