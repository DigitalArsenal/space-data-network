#!/usr/bin/env bash
# fetch-geolite2.sh — stage the GeoLite2-City mmdb for the node status feed.
#
# Source: P3TERX/GeoLite.mmdb GitHub releases — the standard public
# redistribution of MaxMind's GeoLite2 databases (CC BY-SA 4.0 attribution:
# "This product includes GeoLite2 data created by MaxMind, available from
# https://www.maxmind.com"). We pin the fetched file on our IPFS nodes so the
# kubo fork hosts its own copy; hosts read it from SDN_GEOIP_DIR.
#
# Usage:
#   deployment/geoip/fetch-geolite2.sh [dest-dir]   # default: this directory
#
# Prints the sha256 + release date; record both in the deploy log.

set -euo pipefail

DEST="${1:-$(cd "$(dirname "$0")" && pwd)}"
URL="https://github.com/P3TERX/GeoLite.mmdb/releases/latest/download/GeoLite2-City.mmdb"
OUT="$DEST/GeoLite2-City.mmdb"

mkdir -p "$DEST"
echo "[geoip] fetching GeoLite2-City.mmdb -> $OUT"
curl -fsSL --retry 3 -o "$OUT.tmp" "$URL"
mv "$OUT.tmp" "$OUT"
SHA="$(shasum -a 256 "$OUT" | cut -d' ' -f1)"
SIZE="$(du -h "$OUT" | cut -f1)"
echo "[geoip] sha256: $SHA"
echo "[geoip] size:   $SIZE"
echo "[geoip] fetched: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
