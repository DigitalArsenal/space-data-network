# SDN Release And Deployment Pipeline

Production releases are tag-driven and owned by `.github/workflows/release-deploy.yml`.
The pipeline publishes the immutable browser assets first, then builds every
downstream artifact from those exact assets.

## Trust Model

- `release.plg` is the signed binary release publication record.
- `release.pnm` announces the release PLG or release artifact collection CID.
- `release.pnm.SIGNATURE_TYPE` is `secp256k1-sdn-release`.
- `release.pnm.TIMESTAMP_SIGNATURE_TYPE` is `bitcoin`.
- `release-bitcoin.json` binds the `release.plg` SHA-256 hash to Bitcoin
  signature and anchor evidence.
- Supply-chain signatures stay in place: cosign for containers, native package
  signing metadata, checksums, SBOMs, and GitHub provenance.

## Required Artifacts

- `ghcr.io/<owner>/<repo>-full:<version>`
- `ghcr.io/<owner>/<repo>-edge:<version>`
- full-node RPM and DEB
- edge-relay RPM and DEB
- Linux VM tarball
- CycloneDX SBOM: `spacedatanetwork-sbom.cdx.json`
- `ipfs-deployment.json`
- `container-digests.json`
- `release.plg`
- `release.pnm`
- `release-bitcoin.json`
- `spacedatanetwork-checksums.txt`

## Local Verification

```sh
cd sdn-server
../scripts/go-with-wasmedge.sh run ./cmd/spacedatanetwork release verify ../dist/release
```

Manual spot checks:

```sh
cd dist/release
sha256sum -c spacedatanetwork-checksums.txt
cosign verify ghcr.io/<owner>/<repo>-full:<version> \
  --certificate-identity-regexp='https://github.com/<owner>/<repo>/.*' \
  --certificate-oidc-issuer='https://token.actions.githubusercontent.com'
rpm --checksig spacedatanetwork-full-*.rpm
```

## Bitcoin Evidence

`release-bitcoin.json` must include:

- release publication hash;
- Bitcoin signature;
- Bitcoin public key, address, or descriptor;
- network;
- transaction ID or timestamp proof reference;
- output index when applicable;
- block height and confirmation count when known;
- verification instructions.

Supported anchor methods are `op_return`, `taproot`, and
`opentimestamps-bitcoin`. Other methods must be documented before use.

## Product Surfaces

The root product UI, upstream-style `/webui`, and admin `/admin` surface remain
separate. The release workflow copies the IPFS-published `sdn-js/ui/dist` and
`webui/build` directories into Docker/native packages rather than rebuilding
different assets later in the pipeline.
