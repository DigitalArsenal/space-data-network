// Notarize the signed macOS app, and REFUSE to ship a signed-but-unnotarized one.
//
// Gatekeeper does not care that an app is signed. Without a notarization ticket
// it shows "Apple could not verify ... is free of malware", whose only buttons
// are Move to Trash and Done — no open-anyway — so a Developer ID build that
// skipped notarization is indistinguishable, to a user, from the ad-hoc build it
// replaced. That is the failure this hook exists to make impossible: when a
// signing identity IS configured, missing notarization credentials are a build
// error, not a quiet skip.
//
// Without an identity there is nothing to notarize: pkgs/macos/adhoc-sign.js has
// signed the app with "-" so it launches on Apple Silicon, and this returns.
//
// Credentials, in order of preference:
//   APPLE_API_KEY + APPLE_API_KEY_ID + APPLE_API_ISSUER   App Store Connect key
//   APPLE_ID + APPLE_APP_SPECIFIC_PASSWORD + APPLE_TEAM_ID
// The API key is preferred: it does not expire on a password rotation and is
// not tied to one person's Apple ID or their 2FA.
//
// @electron/notarize staples the ticket on success, so the app validates
// offline — a first launch on a machine with no network still opens.
require('dotenv').config()
const { notarize } = require('@electron/notarize')
const { hasSigningIdentity } = require('./signing-identity')

const isSet = (value) => Boolean(value) && value !== 'false'

function credentials () {
  const { APPLE_API_KEY, APPLE_API_KEY_ID, APPLE_API_ISSUER } = process.env
  if (APPLE_API_KEY && APPLE_API_KEY_ID && APPLE_API_ISSUER) {
    return {
      kind: 'App Store Connect API key',
      creds: { appleApiKey: APPLE_API_KEY, appleApiKeyId: APPLE_API_KEY_ID, appleApiIssuer: APPLE_API_ISSUER }
    }
  }
  const { APPLE_ID, APPLE_APP_SPECIFIC_PASSWORD, APPLE_TEAM_ID } = process.env
  if (APPLE_ID && APPLE_APP_SPECIFIC_PASSWORD && APPLE_TEAM_ID) {
    return {
      kind: `Apple ID ${APPLE_ID}`,
      creds: { appleId: APPLE_ID, appleIdPassword: APPLE_APP_SPECIFIC_PASSWORD, teamId: APPLE_TEAM_ID }
    }
  }
  return null
}

exports.default = async function notarizing (context) {
  if (context.electronPlatformName !== 'darwin') return

  if (!hasSigningIdentity()) {
    console.log('  • no signing identity: ad-hoc signed, not notarized')
    return
  }

  // A pull request builds without secrets by design.
  if (isSet(process.env.CI_PULL_REQUEST) || isSet(process.env.CI_PULL_REQUESTS)) return

  const found = credentials()
  if (!found) {
    throw new Error(
      'This build is signed with a Developer ID but has no notarization credentials, ' +
      'and Gatekeeper blocks a signed-but-unnotarized app exactly as hard as an unsigned one. ' +
      'Set APPLE_API_KEY + APPLE_API_KEY_ID + APPLE_API_ISSUER, or ' +
      'APPLE_ID + APPLE_APP_SPECIFIC_PASSWORD + APPLE_TEAM_ID.'
    )
  }

  const appName = context.packager.appInfo.productFilename
  const appPath = `${context.appOutDir}/${appName}.app`
  console.log(`  • notarizing ${appName}.app with ${found.kind} (this takes minutes)`)

  await notarize({
    tool: 'notarytool',
    appPath,
    appBundleId: context.packager.appInfo.id,
    ...found.creds
  })

  console.log(`  • notarized and stapled ${appName}.app`)
}
