// Ad-hoc sign the packaged macOS app when there is no signing identity.
//
// electron-builder skips signing entirely under CSC_IDENTITY_AUTO_DISCOVERY=false,
// and an Apple Silicon Mac refuses to launch a Mach-O whose signature no longer
// matches its contents -- which is every app electron-builder has just rewritten.
// So when no identity is configured, sign with the ad-hoc identity "-".
//
// Order matters. The bundled node and the WasmEdge library it loads live in
// Contents/Resources, where codesign seals them as data rather than descending
// into them, so they are signed FIRST and the app bundle second. They are
// signed WITHOUT the hardened runtime on purpose: the node resolves its
// WasmEdge library through DYLD_LIBRARY_PATH as a fallback, and the hardened
// runtime strips that variable.
const { execFileSync } = require('node:child_process')
const { existsSync, readdirSync, statSync } = require('node:fs')
const { join } = require('node:path')

const MACH_O_MAGIC = ['cafebabe', 'feedface', 'feedfacf', 'cffaedfe', 'cefaedfe']

function hasSigningIdentity () {
  if (process.env.CSC_IDENTITY_AUTO_DISCOVERY === 'false') return false
  return Boolean(process.env.CSC_LINK || process.env.CSC_NAME || process.env.APPLE_TEAM_ID)
}

function isMachO (file) {
  try {
    const output = execFileSync('/usr/bin/xxd', ['-p', '-l', '4', file], { encoding: 'utf8' }).trim()
    return MACH_O_MAGIC.includes(output)
  } catch {
    return false
  }
}

function * walk (dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isSymbolicLink()) continue
    if (entry.isDirectory()) { yield * walk(path); continue }
    if (entry.isFile()) yield path
  }
}

function sign (target, extra = []) {
  execFileSync('/usr/bin/codesign', ['--force', '--sign', '-', '--timestamp=none', ...extra, target], {
    stdio: ['ignore', 'ignore', 'inherit']
  })
}

exports.default = async function adhocSign (context) {
  if (context.electronPlatformName !== 'darwin') return
  if (hasSigningIdentity()) return

  const appName = context.packager.appInfo.productFilename
  const appPath = join(context.appOutDir, `${appName}.app`)
  if (!existsSync(appPath)) return

  const nodeBundle = join(appPath, 'Contents', 'Resources', 'sdn-node')
  if (existsSync(nodeBundle)) {
    let signed = 0
    for (const file of walk(nodeBundle)) {
      if (!statSync(file).isFile() || !isMachO(file)) continue
      sign(file)
      signed += 1
    }
    console.log(`  • ad-hoc signed ${signed} binaries in the node bundle`)
  }

  sign(appPath, ['--deep', '--options', 'runtime', '--entitlements', join(__dirname, 'entitlements.mac.plist')])
  console.log(`  • ad-hoc signed ${appName}.app (no signing identity configured)`)
}
