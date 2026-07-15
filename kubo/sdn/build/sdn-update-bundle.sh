#!/usr/bin/env bash
#
# sdn-update-bundle.sh — assemble an SDN kubo-runtime update bundle.
#
# Follows docs/sdn-signed-updater.md (target.kind: kubo-runtime): a versioned
# bundle directory holding the built SDN-kubo binary + the SDN WASM modules, a
# tar.gz payload, and a manifest.json that pins the target, version, and the
# SHA-256 of every artifact. Signing is Ed25519 over the canonical manifest; if
# no signing key is provided the manifest is emitted UNSIGNED with an explicit
# operator TODO. This tool NEVER fabricates a signature and NEVER contacts
# updates.spacedatanetwork.org (that feed is not yet provisioned — see the doc).
#
# Usage:
#   sdn-update-bundle.sh [--version V] [--channel C] [--sequence N]
#                        [--platform P] [--arch A] [--created-at RFC3339]
#                        [--binary FILE] [--out-dir DIR]
#                        [--key key.pem] [--key-id ID]
#
# Signing key: --key FILE, or $SDN_UPDATE_SIGNING_KEY_PEM (PEM Ed25519 private
# key). Absent -> unsigned bundle + TODO.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MODULE_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
BUILDER="$SCRIPT_DIR/build-sdn-kubo.sh"

log() { printf '[sdn-update-bundle] %s\n' "$*" >&2; }
die() { printf '[sdn-update-bundle] ERROR: %s\n' "$*" >&2; exit 1; }

sha256_of() {  # -> bare hex digest of $1
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}';
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}
size_of() { wc -c < "$1" | tr -d ' '; }

rfc3339_now() { date -u +%Y-%m-%dT%H:%M:%SZ; }
plus_90d() {  # $1 = RFC3339 UTC start -> +90d RFC3339 UTC
  if date -u -d "$1 +90 days" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null; then return 0; fi   # GNU
  date -u -j -v+90d -f %Y-%m-%dT%H:%M:%SZ "$1" +%Y-%m-%dT%H:%M:%SZ                  # BSD/macOS
}

# --- args --------------------------------------------------------------------
VERSION=""; CHANNEL="dev"; SEQUENCE="1"
PLATFORM=""; ARCH=""; CREATED_AT=""
BINARY=""; OUT_DIR="$MODULE_ROOT/dist/update-bundle"
KEY="${SDN_UPDATE_SIGNING_KEY_PEM:-}"; KEY_ID=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)    VERSION="$2"; shift 2 ;;
    --channel)    CHANNEL="$2"; shift 2 ;;
    --sequence)   SEQUENCE="$2"; shift 2 ;;
    --platform)   PLATFORM="$2"; shift 2 ;;
    --arch)       ARCH="$2"; shift 2 ;;
    --created-at) CREATED_AT="$2"; shift 2 ;;
    --binary)     BINARY="$2"; shift 2 ;;
    --out-dir)    OUT_DIR="$2"; shift 2 ;;
    --key)        KEY="$2"; shift 2 ;;
    --key-id)     KEY_ID="$2"; shift 2 ;;
    -h|--help)    grep '^#' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *)            die "unknown argument: $1" ;;
  esac
done

command -v jq >/dev/null 2>&1 || die "jq is required to build manifest.json"

# --- defaults from the tree --------------------------------------------------
if [[ -z "$VERSION" ]]; then
  VERSION="$(sed -n 's/^const CurrentVersionNumber = "\(.*\)".*/\1/p' "$MODULE_ROOT/version.go" | head -1)"
  [[ -n "$VERSION" ]] || VERSION="0.0.0-dev"
fi
[[ -n "$PLATFORM" ]] || PLATFORM="$(cd "$MODULE_ROOT" && go env GOOS)"
[[ -n "$ARCH" ]]     || ARCH="$(cd "$MODULE_ROOT" && go env GOARCH)"
[[ -n "$CREATED_AT" ]] || CREATED_AT="$(rfc3339_now)"
EXPIRES_AT="$(plus_90d "$CREATED_AT")"
KUBO_VERSION="$(sed -n 's/^const CurrentVersionNumber = "\(.*\)".*/\1/p' "$MODULE_ROOT/version.go" | head -1)"

BUNDLE_DIR="$OUT_DIR/kubo-runtime/$CHANNEL/$PLATFORM/$ARCH/$VERSION"
STAGE="$BUNDLE_DIR/payload"
rm -rf "$BUNDLE_DIR"
mkdir -p "$STAGE/bin" "$STAGE/modules"

# --- 1. binary ---------------------------------------------------------------
if [[ -z "$BINARY" ]]; then
  log "no --binary given; building with build-sdn-kubo.sh ..."
  BINARY="$BUNDLE_DIR/.build-ipfs"
  "$BUILDER" --out "$BINARY" >&2
fi
[[ -x "$BINARY" ]] || die "binary not found/executable: $BINARY"
cp -p "$BINARY" "$STAGE/bin/ipfs"
[[ "$BINARY" == "$BUNDLE_DIR/.build-ipfs" ]] && rm -f "$BINARY"

# --- 2. SDN WASM modules -----------------------------------------------------
wasm_count=0
while IFS= read -r w; do
  [[ -z "$w" ]] && continue
  cp -p "$w" "$STAGE/modules/$(basename "$w")"
  wasm_count=$((wasm_count + 1))
done < <(find "$MODULE_ROOT/sdn" -name '*.wasm' -type f | sort)
log "collected $wasm_count SDN WASM module(s)"

# --- 3. per-artifact digests -------------------------------------------------
artifacts_json="[]"
while IFS= read -r f; do
  rel="${f#"$STAGE"/}"
  role="module"; [[ "$rel" == bin/* ]] && role="runtime-binary"
  artifacts_json="$(jq \
    --arg path "$rel" --arg role "$role" \
    --arg sha "$(sha256_of "$f")" --argjson size "$(size_of "$f")" \
    '. += [{"path":$path,"role":$role,"sha256":$sha,"size":$size}]' \
    <<<"$artifacts_json")"
done < <(find "$STAGE" -type f | sort)

# --- 4. tar.gz payload carrier (bundle.hash is over this archive) ------------
BUNDLE_TGZ="$BUNDLE_DIR/bundle.tar.gz"
tar -C "$STAGE" -czf "$BUNDLE_TGZ" .
BUNDLE_HASH="$(sha256_of "$BUNDLE_TGZ")"
BUNDLE_SIZE="$(size_of "$BUNDLE_TGZ")"

# --- 5. manifest.json (unsigned form first) ----------------------------------
UPDATE_ID="sdn-kubo-runtime-$VERSION-$PLATFORM-$ARCH-$SEQUENCE"
[[ -n "$KEY_ID" ]] || KEY_ID="UNSET"
MANIFEST="$BUNDLE_DIR/manifest.json"

jq -n \
  --arg schema "org.spacedatanetwork.update.v1" \
  --arg update_id "$UPDATE_ID" \
  --arg version "$VERSION" \
  --argjson sequence "$SEQUENCE" \
  --arg channel "$CHANNEL" \
  --arg created_at "$CREATED_AT" \
  --arg expires_at "$EXPIRES_AT" \
  --arg platform "$PLATFORM" \
  --arg arch "$ARCH" \
  --arg kubo_version "$KUBO_VERSION" \
  --arg bundle_hash "$BUNDLE_HASH" \
  --argjson bundle_size "$BUNDLE_SIZE" \
  --arg key_id "$KEY_ID" \
  --argjson artifacts "$artifacts_json" \
  '{
     schema: $schema,
     update_id: $update_id,
     version: $version,
     sequence: $sequence,
     channel: $channel,
     created_at: $created_at,
     expires_at: $expires_at,
     target: { platform: $platform, arch: $arch, kind: "kubo-runtime" },
     compatibility: { min_kubo_version: $kubo_version },
     upstream: { kubo: { source: "github.com/ipfs/kubo", version: $kubo_version } },
     bundle: { format: "tar.gz", path: "bundle.tar.gz", hash: $bundle_hash, size: $bundle_size },
     artifacts: $artifacts,
     signing: { algorithm: "Ed25519", key_id: $key_id, status: "UNSIGNED", signature: null }
   }' > "$MANIFEST"

# --- 6. signing (only if a real key is present) ------------------------------
SIGNED="no"
if [[ -n "$KEY" ]]; then
  [[ -f "$KEY" ]] || die "signing key not found: $KEY"
  [[ "$KEY_ID" != "UNSET" ]] || die "--key requires --key-id (or set key_id)"
  # Canonical bytes = sorted-compact manifest with signature omitted.
  CANON="$BUNDLE_DIR/.manifest.canonical.json"
  jq -S -c '.signing.signature = null | .signing.status = "SIGNING"' "$MANIFEST" > "$CANON"
  SIG_B64="$(openssl pkeyutl -sign -inkey "$KEY" -rawin -in "$CANON" 2>/dev/null | openssl base64 -A)" \
    || die "Ed25519 signing failed (is $KEY a raw Ed25519 private key?)"
  jq --arg sig "$SIG_B64" '.signing.status = "SIGNED" | .signing.signature = $sig' "$MANIFEST" > "$MANIFEST.tmp"
  mv "$MANIFEST.tmp" "$MANIFEST"
  rm -f "$CANON"
  SIGNED="yes"
  log "manifest signed with key_id=$KEY_ID"
else
  cat > "$BUNDLE_DIR/SIGNING.TODO.txt" <<EOF
UNSIGNED SDN update bundle.

This bundle's manifest.json has signing.status = "UNSIGNED" and no signature.
Before publishing, a release operator must:

  1. Sign the canonical manifest with the SDN Ed25519 release key:
       jq -S -c '.signing.signature=null | .signing.status="SIGNING"' manifest.json > canonical.json
       openssl pkeyutl -sign -inkey <release-key.pem> -rawin -in canonical.json | openssl base64 -A
     then set signing.status="SIGNED", signing.key_id, and signing.signature.
     (Or re-run: sdn-update-bundle.sh --key <release-key.pem> --key-id <id> ...)

  2. Wrap bundle.tar.gz in the inert WASM carrier (update.wasm) and record
     wasm.hash per docs/sdn-signed-updater.md.

  3. Publish manifest.json + update.wasm to the SDN update feed:
       https://updates.spacedatanetwork.org/kubo-runtime/$CHANNEL/$PLATFORM/$ARCH/$VERSION/
     NOTE: that feed host is not yet provisioned (no DNS). Publication is an
     owner/release-ops step; this tool intentionally does not reach it.
EOF
  log "no signing key -> UNSIGNED bundle; wrote SIGNING.TODO.txt"
fi

# --- report ------------------------------------------------------------------
echo
echo "================= SDN UPDATE BUNDLE ================="
echo "bundle dir : $BUNDLE_DIR"
echo "target     : kubo-runtime / $CHANNEL / $PLATFORM / $ARCH"
echo "version    : $VERSION (seq $SEQUENCE)  kubo $KUBO_VERSION"
echo "signed     : $SIGNED"
echo "artifacts  :"
jq -r '.artifacts[] | "  \(.role)\t\(.size) B\t\(.sha256)  \(.path)"' "$MANIFEST"
echo "bundle.tar.gz: $BUNDLE_SIZE B  sha256 $BUNDLE_HASH"
echo "contents   :"
( cd "$BUNDLE_DIR" && find . -type f | sort | sed 's/^/  /' )
echo "===================================================="
echo "manifest.json:"
cat "$MANIFEST"
