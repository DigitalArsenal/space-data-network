# SDN Release And Deployment Pipeline

Production releases are tag-driven and owned by `.github/workflows/release-deploy.yml`.
The pipeline publishes the immutable browser assets first, then builds every
downstream artifact from those exact assets.

## Beta Releases

Beta artifacts are published by `.github/workflows/beta-release-artifacts.yml`
as a GitHub release with
`make_latest: true`. The default release number is
`v<package.json version>-beta.<run number>`, and manual runs may only provide a
version containing a SemVer `beta` prerelease segment.
Native Linux package asset names replace the SemVer beta hyphen with `.`, so
`v1.0.3-beta.1` is uploaded as `1.0.3.beta.1`.

The beta workflow intentionally stays separate from the production signed
release path. It does not create `release.plg`, `release.pnm`, or Bitcoin
anchor records. It does publish usable artifacts for testers:

- `dockerdigitalarsenal/space-data-network:<beta-version>`
- full-node RPM and DEB packages named with `spacedatanetwork-full`
- edge-relay RPM and DEB packages named with `spacedatanetwork-edge`
- Linux VM tarball named with `spacedatanetwork-linux-vm-`
- downloadable Docker image tarball named with `spacedatanetwork-container-`
- macOS ARM64 bundle named `spacedatanetwork-darwin-arm64.tar.gz`
- browser and Node SDK tarball named with `spacedatanetwork-sdn-js-`
- CycloneDX SBOM: `spacedatanetwork-sbom.cdx.json`
- `ipfs-deployment.json`
- `container-digests.json`
- `spacedatanetwork-beta-manifest.json`
- `spacedatanetwork-checksums.txt`

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

- `dockerdigitalarsenal/space-data-network:<version>`
- full-node RPM and DEB
- edge-relay RPM and DEB
- Linux VM tarball
- downloadable Docker image tarball
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

The Docker artifact harness installs every release artifact in clean containers
loads downloadable Docker image tarballs, smoke-tests their entrypoints, and
starts a small SDN network from the installed native binaries:

```sh
npm run test:release-artifacts:docker -- --release-dir dist/release
```

For local `act` runs, point it at the artifact server directory instead:

```sh
npm run test:release-artifacts:docker -- --act-artifacts /tmp/sdn-beta-act-artifacts
```

Manual spot checks:

```sh
cd dist/release
sha256sum -c spacedatanetwork-checksums.txt
cosign verify dockerdigitalarsenal/space-data-network:<version> \
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
