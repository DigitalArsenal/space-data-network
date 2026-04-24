# Deployment Pipeline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a release pipeline that starts by publishing the IPFS WebUI/admin static payload, then builds and pushes signed Docker images, RPMs, DEBs, tarballs, checksums, SBOMs, and release artifacts for Space Data Network.

**Architecture:** Use one tag-driven GitHub Actions workflow as the release orchestrator, backed by small repo scripts that can also run locally. Keep `/`, `/webui`, and `/admin` packaging inputs separate: `sdn-js/ui/dist` is the SDN UI/admin shell, `webui/build` is the upstream-style IPFS WebUI, and `sdn-server` is the daemon host. Use `nfpm` for native Linux packages so RPM and DEB share one package definition, and keep the existing `deployment/scripts/package-linux-vm-bundle.sh` tarball as a first-class artifact.

**Tech Stack:** GitHub Actions, Kubo CLI, Docker Buildx, GHCR, cosign, syft, nfpm, Go 1.24.x, Node 20, npm, systemd.

---

## File Structure

- Create `.github/workflows/release-deploy.yml`: tag/manual release pipeline that runs IPFS publication first, then Docker images, native packages, checksums, signing, and GitHub Release upload.
- Create `deployment/ipfs/ipfs-deploy.sh`: builds static UI assets, adds `/webui` and SDN UI/admin assets to IPFS, writes CID metadata, and optionally pins to configured remote pinning service.
- Create `deployment/ipfs/pin-kubo.sh`: small Kubo helper for local or CI IPFS add/pin commands.
- Create `deployment/packaging/nfpm.yaml`: shared RPM/DEB package definition for the full node.
- Create `deployment/packaging/nfpm-edge.yaml`: RPM/DEB package definition for edge relay.
- Create `deployment/packaging/build-linux-packages.sh`: Linux package build script used by CI and local release testing.
- Create `deployment/packaging/postinstall.sh`: package post-install hook that creates the `sdn` user, reloads systemd, and enables services without starting unexpectedly.
- Create `deployment/packaging/preremove.sh`: package remove hook that stops services on uninstall.
- Create `deployment/packaging/README.md`: release artifact usage instructions for Docker, RPM, DEB, and tarball installs.
- Modify `deployment/scripts/package-linux-vm-bundle.sh`: accept prebuilt `webui/build` and `sdn-js/ui/dist`, write version metadata, and emit checksum-friendly output paths.
- Modify `.github/workflows/docker-publish.yml`: either retire it after `release-deploy.yml` lands or make it call the same reusable Docker job to avoid two conflicting image pipelines.

## Package Targets

- `ghcr.io/<owner>/<repo>-full:<version>`: full node daemon plus hosted `/` SDN UI and `/webui` IPFS WebUI assets.
- `ghcr.io/<owner>/<repo>-edge:<version>`: edge relay.
- `spacedatanetwork-full-<version>-1.x86_64.rpm`: RPM for RHEL, Fedora, Rocky, Alma.
- `spacedatanetwork-full_<version>_amd64.deb`: DEB for Debian and Ubuntu.
- `spacedatanetwork-edge-<version>-1.x86_64.rpm`: edge relay RPM.
- `spacedatanetwork-edge_<version>_amd64.deb`: edge relay DEB.
- `spacedatanetwork-linux-vm-<version>.tar.gz`: existing generic systemd tarball.
- `spacedatanetwork-checksums.txt`: SHA256 checksums for every non-container artifact.
- `spacedatanetwork-sbom.cdx.json`: CycloneDX SBOM for binaries and package contents.
- `ipfs-deployment.json`: immutable CID manifest for `sdn-js/ui/dist` and `webui/build`.
- Optional later artifacts: Helm chart or Compose bundle for operators who do not want raw Docker commands.

---

### Task 1: Add IPFS Deployment Script

**Files:**
- Create: `deployment/ipfs/ipfs-deploy.sh`
- Create: `deployment/ipfs/pin-kubo.sh`

- [ ] **Step 1: Add the Kubo helper**

Create `deployment/ipfs/pin-kubo.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

TARGET="${1:?usage: pin-kubo.sh /path/to/static-dir}"
NAME="${2:?usage: pin-kubo.sh /path/to/static-dir name}"

if ! command -v ipfs >/dev/null 2>&1; then
  echo "ipfs CLI is required" >&2
  exit 1
fi

CID="$(ipfs add --cid-version=1 --quieter --recursive --pin=true "$TARGET" | tail -n 1)"
echo "{\"name\":\"${NAME}\",\"cid\":\"${CID}\",\"path\":\"/ipfs/${CID}\"}"
```

- [ ] **Step 2: Add the orchestrating IPFS deploy script**

Create `deployment/ipfs/ipfs-deploy.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT_DIR="${OUT_DIR:-${ROOT}/dist/ipfs}"
VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty)}"
PIN_HELPER="${ROOT}/deployment/ipfs/pin-kubo.sh"

mkdir -p "$OUT_DIR"

(cd "$ROOT/sdn-js" && npm ci && npm run build:ui)
(cd "$ROOT/webui" && npm ci && npm run build)

SDN_UI_JSON="$("$PIN_HELPER" "$ROOT/sdn-js/ui/dist" "sdn-ui")"
WEBUI_JSON="$("$PIN_HELPER" "$ROOT/webui/build" "ipfs-webui")"

cat > "${OUT_DIR}/ipfs-deployment.json" <<JSON
{
  "version": "${VERSION}",
  "generatedAt": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "sdnUi": ${SDN_UI_JSON},
  "ipfsWebui": ${WEBUI_JSON}
}
JSON

cat "${OUT_DIR}/ipfs-deployment.json"
```

- [ ] **Step 3: Verify local dry execution up to missing Kubo**

Run:

```bash
bash -n deployment/ipfs/ipfs-deploy.sh
bash -n deployment/ipfs/pin-kubo.sh
```

Expected: both commands exit `0`.

- [ ] **Step 4: Commit**

```bash
git add deployment/ipfs/ipfs-deploy.sh deployment/ipfs/pin-kubo.sh
git commit -m "build: add IPFS deployment scripts"
```

---

### Task 2: Add Native Package Definitions

**Files:**
- Create: `deployment/packaging/nfpm.yaml`
- Create: `deployment/packaging/nfpm-edge.yaml`
- Create: `deployment/packaging/postinstall.sh`
- Create: `deployment/packaging/preremove.sh`

- [ ] **Step 1: Add full-node nfpm config**

Create `deployment/packaging/nfpm.yaml`:

```yaml
name: spacedatanetwork-full
arch: amd64
platform: linux
version: ${VERSION}
section: default
priority: optional
maintainer: Space Data Network <ops@spacedatanetwork.org>
description: Space Data Network full node daemon with hosted SDN UI and IPFS WebUI.
homepage: https://github.com/spacedatanetwork/space-data-network
license: MIT
contents:
  - src: dist/package-root/opt/spacedatanetwork/bin/spacedatanetwork
    dst: /opt/spacedatanetwork/bin/spacedatanetwork
    file_info:
      mode: 0755
  - src: dist/package-root/opt/spacedatanetwork/admin-ui
    dst: /opt/spacedatanetwork/admin-ui
  - src: dist/package-root/opt/spacedatanetwork/webui
    dst: /opt/spacedatanetwork/webui
  - src: config/full-vm.yaml
    dst: /etc/spacedatanetwork/config.yaml
    type: config|noreplace
  - src: sdn-server/deploy/spacedatanetwork.service
    dst: /usr/lib/systemd/system/spacedatanetwork.service
scripts:
  postinstall: deployment/packaging/postinstall.sh
  preremove: deployment/packaging/preremove.sh
```

- [ ] **Step 2: Add edge nfpm config**

Create `deployment/packaging/nfpm-edge.yaml`:

```yaml
name: spacedatanetwork-edge
arch: amd64
platform: linux
version: ${VERSION}
section: default
priority: optional
maintainer: Space Data Network <ops@spacedatanetwork.org>
description: Space Data Network edge relay.
homepage: https://github.com/spacedatanetwork/space-data-network
license: MIT
contents:
  - src: dist/package-root/opt/spacedatanetwork/bin/spacedatanetwork-edge
    dst: /opt/spacedatanetwork/bin/spacedatanetwork-edge
    file_info:
      mode: 0755
  - src: sdn-server/deploy/spacedatanetwork-edge.service
    dst: /usr/lib/systemd/system/spacedatanetwork-edge.service
scripts:
  postinstall: deployment/packaging/postinstall.sh
  preremove: deployment/packaging/preremove.sh
```

- [ ] **Step 3: Add package lifecycle scripts**

Create `deployment/packaging/postinstall.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

if ! id -u sdn >/dev/null 2>&1; then
  useradd --system --home /var/lib/spacedatanetwork --shell /usr/sbin/nologin sdn
fi

mkdir -p /var/lib/spacedatanetwork/data /var/lib/spacedatanetwork/frontend /etc/spacedatanetwork
chown -R sdn:sdn /var/lib/spacedatanetwork /opt/spacedatanetwork

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
  systemctl enable spacedatanetwork.service 2>/dev/null || true
  systemctl enable spacedatanetwork-edge.service 2>/dev/null || true
fi
```

Create `deployment/packaging/preremove.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

if command -v systemctl >/dev/null 2>&1; then
  systemctl stop spacedatanetwork.service 2>/dev/null || true
  systemctl stop spacedatanetwork-edge.service 2>/dev/null || true
fi
```

- [ ] **Step 4: Verify YAML syntax**

Run:

```bash
npx --yes js-yaml deployment/packaging/nfpm.yaml >/dev/null
npx --yes js-yaml deployment/packaging/nfpm-edge.yaml >/dev/null
bash -n deployment/packaging/postinstall.sh
bash -n deployment/packaging/preremove.sh
```

Expected: all commands exit `0`.

- [ ] **Step 5: Commit**

```bash
git add deployment/packaging/nfpm.yaml deployment/packaging/nfpm-edge.yaml deployment/packaging/postinstall.sh deployment/packaging/preremove.sh
git commit -m "build: add native Linux package definitions"
```

---

### Task 3: Add Linux Package Builder

**Files:**
- Create: `deployment/packaging/build-linux-packages.sh`

- [ ] **Step 1: Add package build script**

Create `deployment/packaging/build-linux-packages.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty | sed 's/^v//')}"
OUT_DIR="${OUT_DIR:-${ROOT}/dist/packages}"
PKG_ROOT="${ROOT}/dist/package-root"

rm -rf "$PKG_ROOT" "$OUT_DIR"
mkdir -p "$PKG_ROOT/opt/spacedatanetwork/bin" "$PKG_ROOT/opt/spacedatanetwork/admin-ui" "$PKG_ROOT/opt/spacedatanetwork/webui" "$OUT_DIR"

(cd "$ROOT/sdn-js" && npm ci && npm run build:ui)
(cd "$ROOT/webui" && npm ci && npm run build)
(cd "$ROOT/sdn-server" && "$ROOT/scripts/go-with-wasmedge.sh" build -o "$PKG_ROOT/opt/spacedatanetwork/bin/spacedatanetwork" ./cmd/spacedatanetwork)
(cd "$ROOT/sdn-server" && "$ROOT/scripts/go-with-wasmedge.sh" build -tags edge -o "$PKG_ROOT/opt/spacedatanetwork/bin/spacedatanetwork-edge" ./cmd/spacedatanetwork-edge)

cp -R "$ROOT/sdn-js/ui/dist/." "$PKG_ROOT/opt/spacedatanetwork/admin-ui/"
cp -R "$ROOT/webui/build/." "$PKG_ROOT/opt/spacedatanetwork/webui/"

for format in rpm deb; do
  VERSION="$VERSION" nfpm package --config "$ROOT/deployment/packaging/nfpm.yaml" --packager "$format" --target "$OUT_DIR"
  VERSION="$VERSION" nfpm package --config "$ROOT/deployment/packaging/nfpm-edge.yaml" --packager "$format" --target "$OUT_DIR"
done

find "$OUT_DIR" -type f -maxdepth 1 -print | sort
```

- [ ] **Step 2: Verify shell syntax**

Run:

```bash
bash -n deployment/packaging/build-linux-packages.sh
```

Expected: exits `0`.

- [ ] **Step 3: Commit**

```bash
git add deployment/packaging/build-linux-packages.sh
git commit -m "build: add Linux package builder"
```

---

### Task 4: Update VM Tarball Metadata

**Files:**
- Modify: `deployment/scripts/package-linux-vm-bundle.sh`

- [ ] **Step 1: Add version metadata to the staged bundle**

In `deployment/scripts/package-linux-vm-bundle.sh`, after copying config and service files, add:

```bash
cat > "${STAGE_DIR}/opt/spacedatanetwork/VERSION" <<EOF
version=${VERSION}
git_revision=$(git -C "${PROJECT_ROOT}" rev-parse HEAD)
built_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF
```

- [ ] **Step 2: Keep the existing asset boundary**

Do not change these existing copy operations:

```bash
cp -R "${PROJECT_ROOT}/sdn-js/ui/dist/." "${STAGE_DIR}/opt/spacedatanetwork/admin-ui/"
cp -R "${PROJECT_ROOT}/webui/build/." "${STAGE_DIR}/opt/spacedatanetwork/webui/"
```

They preserve the repo contract that the SDN UI and IPFS WebUI stay separate.

- [ ] **Step 3: Verify on Linux builder**

Run:

```bash
./deployment/scripts/package-linux-vm-bundle.sh
tar -tzf dist/linux-vm/spacedatanetwork-linux-vm-*.tar.gz | grep opt/spacedatanetwork/VERSION
```

Expected: `opt/spacedatanetwork/VERSION` appears in the archive listing.

- [ ] **Step 4: Commit**

```bash
git add deployment/scripts/package-linux-vm-bundle.sh
git commit -m "build: add VM bundle version metadata"
```

---

### Task 5: Add Unified Release Workflow

**Files:**
- Create: `.github/workflows/release-deploy.yml`

- [ ] **Step 1: Add workflow triggers and permissions**

Create `.github/workflows/release-deploy.yml` with:

```yaml
name: Release Deployment

on:
  push:
    tags: ['v*']
  workflow_dispatch:
    inputs:
      version:
        description: 'Version without leading v'
        required: true

permissions:
  contents: write
  packages: write
  id-token: write

env:
  REGISTRY: ghcr.io
  IMAGE_PREFIX: ${{ github.repository }}
```

- [ ] **Step 2: Add the IPFS job first**

Add:

```yaml
jobs:
  ipfs:
    name: Publish static assets to IPFS
    runs-on: ubuntu-latest
    outputs:
      manifest: ${{ steps.manifest.outputs.path }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: '20'
          cache: npm
          cache-dependency-path: |
            sdn-js/package-lock.json
            webui/package-lock.json
      - name: Install Kubo
        run: |
          curl -L https://dist.ipfs.tech/kubo/v0.39.0/kubo_v0.39.0_linux-amd64.tar.gz | tar -xz
          sudo bash kubo/install.sh
          ipfs init --profile=server
          ipfs daemon > /tmp/ipfs.log 2>&1 &
          until ipfs id >/dev/null 2>&1; do sleep 1; done
      - name: Publish assets
        run: deployment/ipfs/ipfs-deploy.sh
      - id: manifest
        run: echo "path=dist/ipfs/ipfs-deployment.json" >> "$GITHUB_OUTPUT"
      - uses: actions/upload-artifact@v4
        with:
          name: ipfs-deployment
          path: dist/ipfs/ipfs-deployment.json
```

- [ ] **Step 3: Add Docker job after IPFS**

Add a `docker` job with `needs: ipfs`, matrix entries for `deployment/docker/Dockerfile.full` and `deployment/docker/Dockerfile.edge`, `docker/build-push-action@v5`, `provenance: true`, `sbom: true`, and cosign signing. Reuse the tagging behavior from `.github/workflows/docker-publish.yml`.

- [ ] **Step 4: Add packages job after IPFS**

Add a `packages` job with:

```yaml
  packages:
    name: Build native packages
    runs-on: ubuntu-latest
    needs: ipfs
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: sdn-server/go.mod
          cache-dependency-path: sdn-server/go.sum
      - uses: actions/setup-node@v4
        with:
          node-version: '20'
          cache: npm
          cache-dependency-path: |
            sdn-js/package-lock.json
            webui/package-lock.json
      - name: Install WasmEdge
        run: ./scripts/install-wasmedge.sh
      - name: Install nfpm and syft
        run: |
          curl -sSfL https://raw.githubusercontent.com/goreleaser/nfpm/main/www/docs/install.sh | sh -s -- -b /usr/local/bin
          curl -sSfL https://raw.githubusercontent.com/anchore/syft/main/install.sh | sh -s -- -b /usr/local/bin
      - name: Build RPM and DEB
        run: deployment/packaging/build-linux-packages.sh
      - name: Build VM tarball
        run: deployment/scripts/package-linux-vm-bundle.sh
      - name: Generate SBOM and checksums
        run: |
          mkdir -p dist/release
          cp dist/packages/* dist/release/
          cp dist/linux-vm/*.tar.gz dist/release/
          cp dist/ipfs/ipfs-deployment.json dist/release/
          syft dir:dist/package-root -o cyclonedx-json=dist/release/spacedatanetwork-sbom.cdx.json
          (cd dist/release && sha256sum * > spacedatanetwork-checksums.txt)
      - uses: actions/upload-artifact@v4
        with:
          name: release-packages
          path: dist/release/*
```

- [ ] **Step 5: Add GitHub Release upload**

Add a final `release` job with `needs: [ipfs, docker, packages]` and `softprops/action-gh-release@v2` that uploads `dist/release/*`.

- [ ] **Step 6: Validate workflow syntax**

Run:

```bash
npx --yes js-yaml .github/workflows/release-deploy.yml >/dev/null
```

Expected: exits `0`.

- [ ] **Step 7: Commit**

```bash
git add .github/workflows/release-deploy.yml
git commit -m "ci: add unified deployment release pipeline"
```

---

### Task 6: Consolidate Existing Docker Workflow

**Files:**
- Modify: `.github/workflows/docker-publish.yml`

- [ ] **Step 1: Avoid duplicate release publishing**

Change `.github/workflows/docker-publish.yml` so it runs only for pull requests and non-tag pushes:

```yaml
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
```

The new `.github/workflows/release-deploy.yml` owns `v*` tag publishing.

- [ ] **Step 2: Keep Docker PR validation**

Preserve buildx, metadata extraction, and build steps, but leave `push: false` for pull requests and `push: true` for `main`.

- [ ] **Step 3: Verify workflow syntax**

Run:

```bash
npx --yes js-yaml .github/workflows/docker-publish.yml >/dev/null
```

Expected: exits `0`.

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/docker-publish.yml
git commit -m "ci: reserve tag releases for deployment pipeline"
```

---

### Task 7: Document Operator Install Paths

**Files:**
- Create: `deployment/packaging/README.md`

- [ ] **Step 1: Add install instructions**

Create `deployment/packaging/README.md` with:

````markdown
# Space Data Network Release Artifacts

## Docker

```bash
docker pull ghcr.io/spacedatanetwork/space-data-network-full:<version>
docker run --rm --network host \
  -v sdn-data:/app/data \
  ghcr.io/spacedatanetwork/space-data-network-full:<version>
```

## RPM

```bash
sudo rpm -Uvh spacedatanetwork-full-<version>-1.x86_64.rpm
sudo systemctl restart spacedatanetwork
```

## DEB

```bash
sudo apt install ./spacedatanetwork-full_<version>_amd64.deb
sudo systemctl restart spacedatanetwork
```

## Generic Linux Tarball

```bash
sudo deployment/scripts/install-vm-bundle.sh spacedatanetwork-linux-vm-<version>.tar.gz
sudo systemctl restart spacedatanetwork
```

## Verification

```bash
sha256sum -c spacedatanetwork-checksums.txt
cosign verify ghcr.io/spacedatanetwork/space-data-network-full:<version> \
  --certificate-identity-regexp='https://github.com/spacedatanetwork/space-data-network/.*' \
  --certificate-oidc-issuer='https://token.actions.githubusercontent.com'
```
````

- [ ] **Step 2: Commit**

```bash
git add deployment/packaging/README.md
git commit -m "docs: document release artifact installation"
```

---

### Task 8: End-to-End Verification

**Files:**
- Test only; no file edits expected.

- [ ] **Step 1: Run focused CI checks**

Run:

```bash
./scripts/ci-local.sh quick
```

Expected: all quick checks pass.

- [ ] **Step 2: Run release script syntax checks**

Run:

```bash
bash -n deployment/ipfs/ipfs-deploy.sh
bash -n deployment/ipfs/pin-kubo.sh
bash -n deployment/packaging/build-linux-packages.sh
bash -n deployment/packaging/postinstall.sh
bash -n deployment/packaging/preremove.sh
npx --yes js-yaml .github/workflows/release-deploy.yml >/dev/null
```

Expected: all commands exit `0`.

- [ ] **Step 3: Run package build on Linux**

Run on an Ubuntu builder with Docker optional but not required:

```bash
./scripts/install-wasmedge.sh
deployment/packaging/build-linux-packages.sh
ls -1 dist/packages
```

Expected: full-node and edge `.rpm` and `.deb` files exist.

- [ ] **Step 4: Run Docker build locally**

Run:

```bash
npm --prefix sdn-js ci
npm --prefix sdn-js run build:ui
npm --prefix webui ci
npm --prefix webui run build
docker build -f deployment/docker/Dockerfile.full -t sdn-full:test .
docker build -f deployment/docker/Dockerfile.edge -t sdn-edge:test .
```

Expected: both images build successfully.

- [ ] **Step 5: Commit verification fixes only if needed**

```bash
git add <fixed-files>
git commit -m "ci: fix deployment pipeline verification"
```

---

## Open Decisions

- Remote IPFS pinning provider: the plan uses local Kubo first. Add Pinata, web3.storage, or an internal cluster after credentials and retention policy are chosen.
- Release channel policy: tag-only releases are safest. Nightly package publishing can be added later with `v0.0.0-nightly.<date>` metadata.
- Multi-arch packages: this plan starts with `linux/amd64`. Add `arm64` after the Go/WasmEdge/native dependency path is verified on Linux ARM builders.
- Helm chart: useful once Kubernetes is a supported operator target; keep it out of the first cut unless there is an immediate cluster deployment.

## Self-Review

- Spec coverage: IPFS runs first, Docker builds and pushes after IPFS, RPM is included, and DEB/tarball/SBOM/checksum artifacts are included as additional deployment packages.
- Repo contract coverage: `/`, `/webui`, and `/admin` assets remain separate; no legacy `/orbpro/*` paths are introduced; no repo-local schema changes are planned.
- Risk: the package hooks intentionally enable services but do not force-start them, avoiding surprise daemon starts during install.
