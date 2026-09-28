// Is a REAL macOS signing identity configured for this build?
//
// One definition, read by both hooks and by scripts/build-local.sh, because
// they must agree: adhoc-sign.js signs with "-" exactly when this is false, and
// notarize-build.js demands notarization credentials exactly when it is true.
// If the two ever disagreed, a build could be ad-hoc signed AND submitted for
// notarization (which fails), or Developer ID signed and silently not notarized
// (which Gatekeeper still blocks, while looking fixed).
//
// It follows electron-builder's own lookup. A CSC_NAME is looked up in the
// keychain whatever CSC_IDENTITY_AUTO_DISCOVERY says. A CSC_LINK certificate is
// found only by discovery, so "false" switches it off. Discovery also searches
// this Mac's keychain, which no variable here can see: a caller whose answer is
// false must set CSC_IDENTITY_AUTO_DISCOVERY=false, as build-local.sh and the
// release workflow do, or electron-builder signs with whatever it finds.
//
// APPLE_TEAM_ID is deliberately NOT accepted as evidence of an identity. It is
// a notarization input, not a certificate: a build with a team id and no cert
// would skip the ad-hoc fallback, get nothing from electron-builder either, and
// ship an app an Apple Silicon Mac refuses to launch at all.
function hasSigningIdentity () {
  if ((process.env.CSC_NAME || '').trim()) return true
  if (process.env.CSC_IDENTITY_AUTO_DISCOVERY === 'false') return false
  return Boolean(process.env.CSC_LINK)
}

module.exports = { hasSigningIdentity }

// `node signing-identity.js` prints developer-id or ad-hoc, for build-local.sh.
if (require.main === module) console.log(hasSigningIdentity() ? 'developer-id' : 'ad-hoc')
