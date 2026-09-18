// Is a REAL macOS signing identity configured for this build?
//
// One definition, imported by both hooks, because they must agree: adhoc-sign.js
// signs with "-" exactly when this is false, and notarize-build.js demands
// notarization credentials exactly when it is true. If the two ever disagreed,
// a build could be ad-hoc signed AND submitted for notarization (which fails),
// or Developer ID signed and silently not notarized (which Gatekeeper still
// blocks, while looking fixed).
//
// APPLE_TEAM_ID is deliberately NOT accepted as evidence of an identity. It is
// a notarization input, not a certificate: a build with a team id and no cert
// would skip the ad-hoc fallback, get nothing from electron-builder either, and
// ship an app an Apple Silicon Mac refuses to launch at all.
function hasSigningIdentity () {
  if (process.env.CSC_IDENTITY_AUTO_DISCOVERY === 'false') return false
  return Boolean(process.env.CSC_LINK || process.env.CSC_NAME)
}

module.exports = { hasSigningIdentity }
