#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "${script_dir}/../.." && pwd)"
out_dir="${OUT_DIR:-${root}/dist/release}"
version="${VERSION:-$(git -C "${root}" describe --tags --always --dirty)}"

mkdir -p "${out_dir}"

cp "${root}/dist/ipfs/ipfs-deployment.json" "${out_dir}/"
cp "${root}/dist/container-digests.json" "${out_dir}/"
cp "${root}/dist/sbom/spacedatanetwork-sbom.cdx.json" "${out_dir}/"
cp "${root}"/dist/packages/* "${out_dir}/"
cp "${root}"/dist/linux-vm/*.tar.gz "${out_dir}/"

(
  cd "${root}/sdn-server"
  ../scripts/go-with-wasmedge.sh run ./cmd/spacedatanetwork release create-records "${out_dir}" \
    --version "${version}" \
    --release-plg-cid "${RELEASE_PLG_CID:?RELEASE_PLG_CID is required}" \
    --release-public-key "${SDN_RELEASE_PUBLIC_KEY:?SDN_RELEASE_PUBLIC_KEY is required}" \
    --bitcoin-signature "${BITCOIN_SIGNATURE:?BITCOIN_SIGNATURE is required}" \
    --bitcoin-public-key "${BITCOIN_PUBLIC_KEY:-}" \
    --bitcoin-address "${BITCOIN_ADDRESS:-}" \
    --bitcoin-descriptor "${BITCOIN_DESCRIPTOR:-}" \
    --bitcoin-network "${BITCOIN_NETWORK:?BITCOIN_NETWORK is required}" \
    --bitcoin-anchor-method "${BITCOIN_ANCHOR_METHOD:?BITCOIN_ANCHOR_METHOD is required}" \
    --bitcoin-txid "${BITCOIN_TXID:-}" \
    --bitcoin-proof "${BITCOIN_PROOF_REFERENCE:-}" \
    --bitcoin-output-index "${BITCOIN_OUTPUT_INDEX:-}" \
    --bitcoin-block-height "${BITCOIN_BLOCK_HEIGHT:-}" \
    --bitcoin-confirmations "${BITCOIN_CONFIRMATIONS:-}"
)

(
  cd "${out_dir}"
  rm -f spacedatanetwork-checksums.txt
  sha256sum * > spacedatanetwork-checksums.txt
)

if [[ -n "${SDN_UPDATE_FEED_ENTRIES:-}" ]]; then
  feed_args=(
    --out-dir "${SDN_UPDATE_FEED_OUT_DIR:-${out_dir}/update-feed}"
  )
  if [[ -n "${SDN_UPDATE_FEED_GENERATED_AT:-}" ]]; then
    feed_args+=(--generated-at "${SDN_UPDATE_FEED_GENERATED_AT}")
  fi
  IFS=',' read -r -a feed_entries <<< "${SDN_UPDATE_FEED_ENTRIES}"
  for feed_entry in "${feed_entries[@]}"; do
    feed_args+=(--entry "${feed_entry}")
  done
  node "${root}/deployment/release/build-sdn-update-feed.js" "${feed_args[@]}"
fi

(
  cd "${root}/sdn-server"
  ../scripts/go-with-wasmedge.sh run ./cmd/spacedatanetwork release verify "${out_dir}"
)
