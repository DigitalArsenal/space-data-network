#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "=== Space Data Network Dependency Updater ==="
echo ""

# Track old and new versions for summary
declare -A OLD_VERSIONS
declare -A NEW_VERSIONS

##############################################################################
# 1. Fetch latest npm versions
##############################################################################
echo "Fetching latest npm package versions..."

LATEST_FLATC_WASM=$(npm view flatc-wasm version 2>/dev/null || echo "UNKNOWN")
LATEST_HD_WALLET_WASM=$(npm view hd-wallet-wasm version 2>/dev/null || echo "UNKNOWN")
LATEST_SDS=$(npm view spacedatastandards.org version 2>/dev/null || echo "UNKNOWN")
LATEST_FLATBUFFERS_NPM=$(npm view flatbuffers version 2>/dev/null || echo "UNKNOWN")

echo "  flatc-wasm:             $LATEST_FLATC_WASM"
echo "  hd-wallet-wasm:         $LATEST_HD_WALLET_WASM"
echo "  spacedatastandards.org:  $LATEST_SDS"
echo "  flatbuffers (npm):      $LATEST_FLATBUFFERS_NPM"
echo ""

##############################################################################
# 2. Fetch latest Go module versions from Go proxy
##############################################################################
echo "Fetching latest Go module versions..."

LATEST_FLATBUFFERS_GO=$(curl -sL "https://proxy.golang.org/github.com/google/flatbuffers/@latest" | node -e "
  let d=''; process.stdin.on('data',c=>d+=c); process.stdin.on('end',()=>{
    try { console.log(JSON.parse(d).Version); } catch(e) { console.log('UNKNOWN'); }
  });
" 2>/dev/null || echo "UNKNOWN")

echo "  flatbuffers (go):       $LATEST_FLATBUFFERS_GO"
echo ""

##############################################################################
# 3. Update sdn-js/package.json with latest hd-wallet-wasm
##############################################################################
SDN_JS_PKG="$REPO_ROOT/sdn-js/package.json"
if [ -f "$SDN_JS_PKG" ]; then
  OLD_HD_WALLET=$(node -e "console.log(JSON.parse(require('fs').readFileSync('$SDN_JS_PKG','utf8')).dependencies['hd-wallet-wasm'] || 'N/A')")
  OLD_VERSIONS[hd-wallet-wasm-sdn-js]="$OLD_HD_WALLET"
  NEW_VERSIONS[hd-wallet-wasm-sdn-js]="$LATEST_HD_WALLET_WASM"

  echo "Updating sdn-js/package.json: hd-wallet-wasm $OLD_HD_WALLET -> $LATEST_HD_WALLET_WASM"
  node -e "
    const fs = require('fs');
    const pkg = JSON.parse(fs.readFileSync('$SDN_JS_PKG', 'utf8'));
    pkg.dependencies['hd-wallet-wasm'] = '$LATEST_HD_WALLET_WASM';
    fs.writeFileSync('$SDN_JS_PKG', JSON.stringify(pkg, null, 2) + '\n');
  "
else
  echo "WARNING: $SDN_JS_PKG not found, skipping."
fi

##############################################################################
# 4. Update sdn-server/go.mod with latest flatbuffers version
##############################################################################
SDN_SERVER_GOMOD="$REPO_ROOT/sdn-server/go.mod"
if [ -f "$SDN_SERVER_GOMOD" ]; then
  OLD_FB_GO=$(grep 'github.com/google/flatbuffers ' "$SDN_SERVER_GOMOD" | awk '{print $2}' || echo "N/A")
  OLD_VERSIONS[flatbuffers-go]="$OLD_FB_GO"
  NEW_VERSIONS[flatbuffers-go]="$LATEST_FLATBUFFERS_GO"

  if [ "$LATEST_FLATBUFFERS_GO" != "UNKNOWN" ]; then
    echo "Updating sdn-server/go.mod: flatbuffers $OLD_FB_GO -> $LATEST_FLATBUFFERS_GO"
    sed -i.bak "s|github.com/google/flatbuffers $OLD_FB_GO|github.com/google/flatbuffers $LATEST_FLATBUFFERS_GO|" "$SDN_SERVER_GOMOD"
    rm -f "${SDN_SERVER_GOMOD}.bak"
  else
    echo "WARNING: Could not determine latest Go flatbuffers version, skipping."
  fi
else
  echo "WARNING: $SDN_SERVER_GOMOD not found, skipping."
fi

echo ""

##############################################################################
# 5. Run npm install in sdn-js
##############################################################################
if [ -f "$SDN_JS_PKG" ]; then
  echo "Running npm install in sdn-js..."
  (cd "$REPO_ROOT/sdn-js" && npm install --no-audit --no-fund 2>&1) || echo "WARNING: npm install failed in sdn-js"
  echo ""
fi

##############################################################################
# 6. Run go mod tidy in sdn-server
##############################################################################
if [ -f "$SDN_SERVER_GOMOD" ]; then
  echo "Running go mod tidy in sdn-server..."
  (cd "$REPO_ROOT/sdn-server" && go mod tidy 2>&1) || echo "WARNING: go mod tidy failed in sdn-server"
  echo ""
fi

##############################################################################
# 7. Print summary
##############################################################################
echo ""
echo "==============================================================================="
echo "                         DEPENDENCY UPDATE SUMMARY"
echo "==============================================================================="
printf "%-35s %-25s %-25s\n" "PACKAGE" "OLD VERSION" "NEW VERSION"
echo "-------------------------------------------------------------------------------"

printf "%-35s %-25s %-25s\n" "flatc-wasm (npm latest)" "" "$LATEST_FLATC_WASM"
printf "%-35s %-25s %-25s\n" "hd-wallet-wasm (sdn-js)" "${OLD_VERSIONS[hd-wallet-wasm-sdn-js]:-N/A}" "${NEW_VERSIONS[hd-wallet-wasm-sdn-js]:-N/A}"
printf "%-35s %-25s %-25s\n" "spacedatastandards.org (npm latest)" "" "$LATEST_SDS"
printf "%-35s %-25s %-25s\n" "flatbuffers (npm latest)" "" "$LATEST_FLATBUFFERS_NPM"
printf "%-35s %-25s %-25s\n" "flatbuffers (sdn-server go.mod)" "${OLD_VERSIONS[flatbuffers-go]:-N/A}" "${NEW_VERSIONS[flatbuffers-go]:-N/A}"
echo "==============================================================================="
echo ""
echo "Done. Please review changes and run tests before committing."
