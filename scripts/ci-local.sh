#!/usr/bin/env bash
# Canonical local CI runner.
# This script is intentionally aligned with .github/workflows/ci.yml.
#
# ONE SCRIPT, TWO CALLERS. A developer runs `quick` (the pre-push gate); CI
# runs the same modes, one per job. Splitting CI into parallel jobs is a
# job-shape change only — every check still lives here, and no check lives in
# the workflow file.
#
#   .github/workflows/ci.yml job   modes it runs
#   ----------------------------   ---------------------------
#   preflight                      preflight, encryption
#   go-quick                       go
#   go-heavy                       heavy
#   go-builds                      builds
#   js                             js, delivery, demo
#   kubo-bolt-on                   kubo
#
# WHERE THEY DIVERGE, AND WHY. `quick` is all of the above except `heavy` —
# internal/storage is ~5 minutes on its own and a pre-push gate should not
# carry it, which is exactly why it is a separate CI job rather than a dropped
# check. `race` is the other way round: local/`full` only, no CI job, as
# before. `full` is the union of everything.
#
# Usage:
#   ./scripts/ci-local.sh quick      # default: everything CI runs except the heavy lane
#   ./scripts/ci-local.sh heavy      # the heavy Go packages (internal/storage) with the budget they need
#   ./scripts/ci-local.sh full       # quick + heavy + race
#   ./scripts/ci-local.sh preflight  # OSS preflight + gofmt only (no toolchain needed)
#   ./scripts/ci-local.sh go         # fast go checks only
#   ./scripts/ci-local.sh builds     # node + edge-relay builds only
#   ./scripts/ci-local.sh race       # CI-only/full race suite
#   ./scripts/ci-local.sh js         # sdn-js checks only
#   ./scripts/ci-local.sh kubo-pin   # the shipped Kubo version pin only
#   ./scripts/ci-local.sh delivery   # focused module-delivery compatibility checks
#   ./scripts/ci-local.sh plugin     # legacy alias for delivery
#   ./scripts/ci-local.sh demo       # plugin-demo integration tests only
#   ./scripts/ci-local.sh encryption # tests/encryption/go

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MODE="${1:-quick}"

RED='\033[0;31m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
NC='\033[0m'

step() { echo -e "\n${CYAN}=== $1 ===${NC}"; }
pass() { echo -e "${GREEN}PASS${NC}: $1"; }
fail() {
  echo -e "${RED}FAIL${NC}: $1" >&2
  exit 1
}

existing_file() {
  local candidate
  for candidate in "$@"; do
    if [[ -n "$candidate" && -f "$candidate" ]]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

nearest_stack_root() {
  local dir="$ROOT"
  while true; do
    if [[ -f "$dir/docs/repository-catalog.md" && -d "$dir/repos/main-packages" ]]; then
      printf '%s\n' "$dir"
      return 0
    fi
    local parent
    parent="$(dirname "$dir")"
    if [[ "$parent" == "$dir" ]]; then
      return 1
    fi
    dir="$parent"
  done
}

ensure_npm_deps() {
  local dir="$1"
  shift || true
  local required_bins=("$@")
  local lockfile="$dir/package-lock.json"
  local npm_cache="$ROOT/.npm-cache"

  if [[ "${CI:-}" == "true" || "${CI:-}" == "1" ]]; then
    if [[ -f "$lockfile" ]]; then
      (cd "$dir" && npm_config_cache="$npm_cache" npm ci)
    else
      (cd "$dir" && npm_config_cache="$npm_cache" npm install --no-audit --no-fund)
    fi
    return
  fi

  if [[ ! -d "$dir/node_modules" ]]; then
    echo "Missing dependencies in $dir/node_modules"
    echo "Install first, then rerun:"
    if [[ -f "$lockfile" ]]; then
      echo "  (cd \"$dir\" && npm ci)"
    else
      echo "  (cd \"$dir\" && npm install)"
    fi
    return 1
  fi

  if [[ ${#required_bins[@]} -gt 0 ]]; then
    for bin in "${required_bins[@]}"; do
      if [[ ! -x "$dir/node_modules/.bin/$bin" ]]; then
        echo "Missing required tool '$bin' in $dir/node_modules/.bin"
        echo "Reinstall dependencies:"
        if [[ -f "$lockfile" ]]; then
          echo "  (cd \"$dir\" && npm ci)"
        else
          echo "  (cd \"$dir\" && npm install)"
        fi
        return 1
      fi
    done
  fi

  echo "Using existing dependencies in $dir/node_modules"
}

prepare_go_wasm_artifacts() {
  step "Go WASM artifacts"

  if [[ -z "${HD_WALLET_WASM_PATH:-}" || ! -f "${HD_WALLET_WASM_PATH:-}" ]]; then
    local hd_wallet_path
    if hd_wallet_path="$(existing_file \
      "$ROOT/sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm" \
      "$ROOT/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm" \
      "$ROOT/../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm" \
      "$ROOT/../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm" \
      "$ROOT/../../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm")"; then
      export HD_WALLET_WASM_PATH="$hd_wallet_path"
    else
      echo "Pure HD wallet WASI artifact not found; artifact-dependent Go tests will skip."
    fi
  fi

  if [[ -f "${HD_WALLET_WASM_PATH:-}" ]]; then
    echo "HD_WALLET_WASM_PATH=$HD_WALLET_WASM_PATH"
  fi

  if [[ -z "${ORBPRO_LICENSING_WASM_PATH:-}" || ! -f "${ORBPRO_LICENSING_WASM_PATH:-}" ]]; then
    local licensing_path
    local licensing_candidates=()
    local stack_root
    if stack_root="$(nearest_stack_root)"; then
      licensing_candidates+=(
        "$stack_root/repos/main-packages/space-data-network-modules/licensing/core/dist/isomorphic/module.wasm"
        "$stack_root/repos/ancillary-packages/space-data-network-modules/licensing/core/dist/isomorphic/module.wasm"
      )
    fi
    licensing_candidates+=(
      "$ROOT/../space-data-network-modules/licensing/core/dist/isomorphic/module.wasm"
      "$ROOT/../../space-data-network-modules/licensing/core/dist/isomorphic/module.wasm"
      "$ROOT/../space-data-network-plugins/licensing/core/dist/isomorphic/module.wasm"
      "$ROOT/../../space-data-network-plugins/licensing/core/dist/isomorphic/module.wasm"
    )
    if licensing_path="$(existing_file "${licensing_candidates[@]}")"; then
      export ORBPRO_LICENSING_WASM_PATH="$licensing_path"
      echo "ORBPRO_LICENSING_WASM_PATH=$ORBPRO_LICENSING_WASM_PATH"
    else
      echo "Licensing WASM artifact not found; artifact-dependent Go tests will skip."
    fi
  else
    echo "ORBPRO_LICENSING_WASM_PATH=$ORBPRO_LICENSING_WASM_PATH"
  fi

  pass "go wasm artifacts"
}

run_preflight() {
  # FIRST, and LOCAL-ONLY, because a workflow file GitHub cannot parse fails
  # with zero jobs and no log line saying why — it reads exactly like a test
  # failure and is not. A workflow cannot catch the defect that stopped it
  # from starting, so this gate is only worth anything before the push; it
  # also keeps the preflight lane off a root `npm ci` for one dev dependency.
  if [[ "${CI:-}" != "true" && "${CI:-}" != "1" ]]; then
    step "Workflow files parse"
    (cd "$ROOT" && node scripts/check-workflow-yaml.mjs)
    pass "workflow files parse"
  fi

  step "OSS preflight"
  (cd "$ROOT" && ./scripts/oss-preflight.sh)
  pass "oss-preflight"

  # The preflight runs the dependency-drift checks themselves; these are the
  # checks' own suites, so a broken check cannot pass by failing open.
  step "Dependency-drift check suites"
  (cd "$ROOT" && node --test \
    scripts/check-sdn-js-dependency-layering.test.mjs \
    scripts/check-npm-audit.test.mjs \
    scripts/check-govulncheck.test.mjs \
    scripts/check-no-orphan-test-suites.test.mjs)
  pass "dependency-drift check suites"

  # Release-assembly logic, which decides what a published release CONTAINS.
  # assemble-beta-release-artifacts.test.mjs was run by no script and no
  # workflow; a test nothing runs is a comment. Both suites are pure Node over
  # temp directories — no toolchain, no network — so they belong in the
  # cheapest gate there is.
  step "Release artifact check suites"
  (cd "$ROOT" && node --test \
    deployment/release/assemble-beta-release-artifacts.test.mjs \
    scripts/merge-mac-update-feed.test.mjs)
  pass "release artifact check suites"

  # THE SUITES NOTHING RAN. A 2026-09-18 sweep found 35 of the 43 root-level
  # *.test.mjs files were named by no npm script, no workflow and not by this
  # one — covering what a release contains, how it installs, how the update
  # manifest is signed, how the public host route renders. 29 passed; they had
  # simply never been run, and two of that day's bugs sat squarely under them.
  # Together they cost about 70 seconds.
  step "Release and deployment check suites"
  (cd "$ROOT" && node --test \
    deployment/celestrak/service-units.test.mjs \
    deployment/public-origin/nginx-smoke.test.mjs \
    deployment/public-origin/render-nginx.test.mjs \
    deployment/release/build-cli-update-payload.test.mjs \
    deployment/release/build-self-contained-cli.test.mjs \
    deployment/release/build-update-carrier.test.mjs \
    deployment/release/extract-release-binary.test.mjs \
    deployment/release/install-script.test.mjs \
    deployment/release/live-dht-client-smoke.test.mjs \
    deployment/release/live-dht-summary.test.mjs \
    deployment/release/live-dht-workflow.test.mjs \
    deployment/release/prepare-beta-release.test.mjs \
    deployment/release/publish-fleet-update.test.mjs \
    deployment/release/published-install-smoke.test.mjs \
    deployment/release/sign-update-manifest.test.mjs \
    deployment/release/test-release-artifacts-docker.test.mjs \
    deployment/release/update-manifest-statement-domain.test.mjs \
    deployment/release/verify-release-binary.test.mjs \
    deployment/spaceaware/install-public-host-route.test.mjs \
    deployment/spaceaware/install-spaceaware-public-host-route.test.mjs \
    deployment/spaceaware/migrate-kubo-repo.test.mjs \
    deployment/spaceaware/nginx-public-host-route-smoke.test.mjs \
    deployment/spaceaware/verify-spaceaware-public-host-route.test.mjs \
    scripts/check-no-app-specific-go.test.mjs \
    scripts/purge-legacy-supplemental-omm-state.test.mjs \
    tests/isomorphic/artifact-crypto.test.mjs)
  pass "release and deployment check suites"

  # And the class cannot come back: every root-level suite must be named by
  # something that runs it, or quarantined with the reason it cannot.
  step "No orphan test suites"
  (cd "$ROOT" && node scripts/check-no-orphan-test-suites.mjs)
  pass "no orphan test suites"
}

# The shipped Kubo pin. Cheap, and it closes a hole that stayed open for
# months: the node reported `kubo_version` from the in-repo fork's
# version.go while every release path downloaded a stock upstream v0.39.0,
# and NOTHING compared the two — neither this script nor any workflow
# mentioned kubo at all.
#
# Deliberately its own gate rather than `npm run check:versions`. That script
# also reaches the network (npm view, go list, git ls-remote) and currently
# reports a pre-existing, unrelated flatsql mismatch, so making the pre-push
# gate depend on all of it would trade one silent hole for a lane that is red
# for reasons nobody here introduced.
# The published site (GitHub Pages serves main:/docs at spacedatanetwork.org)
# advertised v1.0.5-beta.1 while the project shipped sixty-odd releases past it:
# every Download button handed out an artifact from a different build, and one
# container link had 404'd since the page was written. Links resolve or they do
# not; a link to the WRONG build is worse, because nobody reports it.
#
# Network-dependent (it asks GitHub for the newest release), so it warns rather
# than fails when offline — a developer without a network is not a stale site.
run_docs_release() {
  step "published site advertises the newest release"
  if ! node "$ROOT/scripts/sync-docs-release.mjs" --check; then
    fail "docs/ advertises a release that is not the newest — run: node scripts/sync-docs-release.mjs"
  fi
  pass "docs release links"
}

run_kubo_pin() {
  step "Shipped Kubo version pin"
  (cd "$ROOT" && node scripts/check-kubo-pin.js)
  pass "kubo pin"
}

# THE ENGINE MUST NOT RUN INTERPRETED.
#
# The record store opens the FlatSQL engine with WithPrecompiledAOTCache
# (internal/storage/flatsql_boot_state.go): it LOADS an AOT artifact when one
# is in os.UserCacheDir()/flatsql-aot and NEVER compiles on a miss
# (internal/flatsqlrt/aot.go). A checkout that has never prewarmed therefore
# runs every store-touching test through the INTERPRETER, and the only symptom
# is slowness — `go test` swallows a passing package's output, so the
# "FlatSQL engine mode: INTERPRETED" warning never reaches the log.
#
# That was the whole of the CI blowup, and it is the same tax on a fresh
# `git clone`. Measured here 2026-09-18, ONE prebuilt test binary run twice,
# warm cache vs an empty HOME — both runs PASS, the only difference is time:
# internal/directory 8.21s -> 90.08s (11.0x), internal/assetpin 3.56s ->
# 18.80s (5.3x). CI ran cold on every commit: 105.8 minutes, with
# cmd/spacedatanetwork, internal/api, internal/storefront and internal/storage
# all hitting their go-test timeout.
#
# So every lane that runs Go tests prewarms first and ASSERTS the artifact is
# on disk. The assert is the point, not the prewarm: a prewarm that resolved a
# different HOME than the test process leaves exactly the silent
# interpretation this exists to kill — how host-01 lost 8 hours in 2026-09.
# Cheap to be certain: 0.25s when the artifact is already there, ~49s to
# compile it from scratch.
prewarm_engine_aot() {
  step "FlatSQL engine AOT artifact (prewarm + assert)"

  local bin="$ROOT/.ci-artifacts/spacedatanetwork-prewarm"
  mkdir -p "$ROOT/.ci-artifacts"
  "$ROOT/scripts/go-with-wasmedge.sh" build -o "$bin" ./cmd/spacedatanetwork

  local out artifact
  if ! out="$("$bin" prewarm-aot 2>&1)"; then
    printf '%s\n' "$out"
    fail "prewarm-aot failed — the linked libwasmedge has no AOT (LLVM) compiler, or compilation failed"
  fi
  printf '%s\n' "$out" | grep -v '^lld: warning' || true

  artifact="$(printf '%s\n' "$out" | sed -n 's/^[[:space:]]*flatsql engine: \(.*\) (.*)$/\1/p' | tail -n1)"
  if [[ -z "$artifact" ]]; then
    fail "prewarm-aot printed no engine artifact path; the Go suites would run INTERPRETED"
  fi
  if [[ ! -s "$artifact" ]]; then
    fail "prewarm-aot reported $artifact but nothing is there; the Go suites would run INTERPRETED"
  fi
  pass "flatsql engine AOT artifact: $artifact"
}

# prepare_go_toolchain [no-aot]
#   no-aot skips the prewarm for a lane that compiles but never executes the
#   engine (the build lane), which would otherwise pay for an artifact nothing
#   reads.
prepare_go_toolchain() {
  local aot_mode="${1:-aot}"

  prepare_go_wasm_artifacts

  step "WasmEdge headers/libs"
  if [[ -z "${WASMEDGE_DIR:-}" ]]; then
    fail "WASMEDGE_DIR must point to existing WasmEdge headers/libs; automatic local runtime installation is disabled"
  fi
  if [[ ! -f "$WASMEDGE_DIR/include/wasmedge/wasmedge.h" || ! -d "$WASMEDGE_DIR/lib" ]]; then
    fail "invalid WASMEDGE_DIR: $WASMEDGE_DIR"
  fi
  pass "wasmedge headers/libs"

  step "Go deps"
  "$ROOT/scripts/go-with-wasmedge.sh" mod download
  pass "go mod download"

  if [[ "$aot_mode" != "no-aot" ]]; then
    prewarm_engine_aot
  fi
}

# Per-package timings for every go-test lane: printed at the end of the lane,
# and appended to the GitHub step summary when CI exports GITHUB_STEP_SUMMARY.
# The -timeout budgets below are set FROM this table, and it is what keeps
# them honest: a package that doubles shows up here long before it starts
# tripping a timeout, and a lane that starts running INTERPRETED again is a
# 10x column, not a mystery.
report_pkg_timings() {
  local label="$1" log="$2"
  local table
  table="$(awk '($1 == "ok" || $1 == "FAIL") && $3 ~ /^[0-9.]+s$/ {
      d = $3; sub(/s$/, "", d)
      pkg = $2; sub("github.com/spacedatanetwork/sdn-server/", "", pkg)
      printf "%s\t%s\t%s\n", d, $1, pkg
    }' "$log" | sort -rn)"
  [[ -n "$table" ]] || return 0
  {
    printf '\n### go test timings (%s)\n\n' "$label"
    printf '| seconds | result | package |\n| ---: | :--- | :--- |\n'
    printf '%s\n' "$table" | awk -F'\t' '{ printf "| %s | %s | %s |\n", $1, $2, $3 }'
    printf '%s\n' "$table" | awk -F'\t' '{ t += $1; n++ } END { printf "| **%.1f** | **total** | **%d packages** |\n", t, n }'
  } | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
}

# go test, keeping the lane's log so report_pkg_timings can read it. The exit
# status is preserved: the table must be produced for a FAILING lane too,
# which is exactly when someone needs to know what ran long.
go_test_with_timings() {
  local label="$1"; shift
  local log="$ROOT/.ci-artifacts/gotest-${label}.log"
  local rc=0
  mkdir -p "$ROOT/.ci-artifacts"
  set +e
  "$ROOT/scripts/go-with-wasmedge.sh" test "$@" 2>&1 | tee "$log"
  rc=${PIPESTATUS[0]}
  set -e
  report_pkg_timings "$label" "$log"
  return "$rc"
}

# HEAVY_GO_PACKAGES are split out of the quick lane. The original reason was
# that internal/storage blew the quick lane's per-package budget
# (.gotest-full.log, 2026-08-30) and made the gate fail every time, so pushes
# went unguarded — but that was the INTERPRETED engine talking: warm, the
# whole package is 296.6s, inside the quick budget.
#
# It stays separate for the reason that survives the fix: it is the single
# longest package in the repo, and on CI the two lanes are parallel jobs, so
# pulling it out is ~9 minutes off the critical path rather than a check
# anybody skips. Locally `quick` leaves it out and `full` includes it.
HEAVY_GO_PACKAGES="github.com/spacedatanetwork/sdn-server/internal/storage"

heavy_pkg_filter() {
  local filter=""
  for pkg in $HEAVY_GO_PACKAGES; do
    filter="${filter}${filter:+|}^${pkg}\$"
  done
  echo "$filter"
}

# BUDGETS, SET FROM MEASUREMENT — see report_pkg_timings.
#
# `go test -timeout` is PER TEST BINARY, i.e. per package, not per lane. The
# old 20m/90m pair was sized around the INTERPRETED engine and means nothing
# now. Everything below was measured on 2026-09-18, on this repo at 050e58da7:
#
#   quick lane, warm, -p=1 ... 427.6s of test time (548s wall) on one run,
#                              492.5s on a second; the difference is almost
#                              all internal/flatsqlrt (5.8s vs 58.2s), which
#                              compiles its OWN test AOT artifact on a miss
#                              and so self-heals instead of staying slow.
#     largest: internal/api 138.3s / 148.3s, internal/storefront 67.3s /
#              74.7s, cmd/spacedatanetwork 70.0s / 52.0s
#   heavy lane, warm ......... internal/storage 296.59s
#
# A GitHub ubuntu-latest runner is slower than this box, and the factor is
# measured rather than assumed — same package, INTERPRETED on both sides, so
# only the hardware differs: internal/directory 170.409s on CI (run
# 35310122091) against 90.08s here = 1.89x; internal/assetpin 34.716s against
# 18.80s here = 1.85x.
#
# So the worst quick package projects to 148.3s x 1.9 = ~282s of runner time
# and the heavy lane to 296.6s x 1.9 = ~564s. Budgets are ~2.1x and ~2.7x of
# that — enough that ordinary runner variance never trips them, small enough
# that a package which has genuinely broken shows up in minutes instead of
# after an hour. The timings table says which package if it ever does.
QUICK_GO_TEST_TIMEOUT="10m"
HEAVY_GO_TEST_TIMEOUT="25m"
# Unchanged at 30m and NOT measured: -race is local/`full` only, no CI lane
# runs it, and I have no timing for it. Do not treat this one as sized.
RACE_GO_TEST_TIMEOUT="30m"

run_gofmt() {
  step "gofmt (hand-written Go; generated bindings excluded)"
  local unformatted
  unformatted=$(cd "$ROOT/sdn-server" && git ls-files '*.go' | grep -vE '^(third_party/|internal/sds/|internal/status/nst/)' | xargs gofmt -l)
  if [ -n "$unformatted" ]; then
    echo "$unformatted" | sed 's/^/  /'
    fail "gofmt: the files above are not formatted (run gofmt -w)"
  fi
  pass "gofmt"
}

run_go() {
  prepare_go_toolchain
  run_gofmt

  step "Go tests (quick: every package but the heavy set)"
  local pkgs
  pkgs=$("$ROOT/scripts/go-with-wasmedge.sh" list ./... | grep -Ev "$(heavy_pkg_filter)")
  # shellcheck disable=SC2086
  go_test_with_timings quick -p=1 -timeout="$QUICK_GO_TEST_TIMEOUT" -count=1 $pkgs \
    || fail "go test (quick)"
  pass "go test (quick)"
}

run_go_heavy() {
  prepare_go_toolchain

  step "Go tests (heavy: $HEAVY_GO_PACKAGES)"
  # shellcheck disable=SC2086
  go_test_with_timings heavy -p=1 -timeout="$HEAVY_GO_TEST_TIMEOUT" -count=1 $HEAVY_GO_PACKAGES \
    || fail "go test (heavy)"
  pass "go test (heavy)"
}

run_go_race() {
  prepare_go_toolchain

  step "Go tests (race)"
  go_test_with_timings race -race -p=1 -timeout="$RACE_GO_TEST_TIMEOUT" -count=1 ./... \
    || fail "go test -race"
  pass "go test -race"
}

run_go_builds() {
  # no-aot: this lane compiles, it never runs the engine.
  prepare_go_toolchain no-aot

  step "Go build (full node)"
  "$ROOT/scripts/go-with-wasmedge.sh" build -o /tmp/spacedatanetwork ./cmd/spacedatanetwork
  pass "go build spacedatanetwork"

  step "Go build (edge relay)"
  "$ROOT/scripts/go-with-wasmedge.sh" build -tags edge -o /tmp/spacedatanetwork-edge ./cmd/spacedatanetwork-edge
  pass "go build spacedatanetwork-edge"
}

run_sdn_js() {
  step "sdn-js install"
  ensure_npm_deps "$ROOT/sdn-js" eslint vitest tsup
  pass "sdn-js npm ci"

  local eslint_config=""
  for cfg in \
    "$ROOT/sdn-js/eslint.config.js" \
    "$ROOT/sdn-js/eslint.config.cjs" \
    "$ROOT/sdn-js/eslint.config.mjs" \
    "$ROOT/sdn-js/.eslintrc" \
    "$ROOT/sdn-js/.eslintrc.js" \
    "$ROOT/sdn-js/.eslintrc.cjs" \
    "$ROOT/sdn-js/.eslintrc.json" \
    "$ROOT/sdn-js/.eslintrc.yml" \
    "$ROOT/sdn-js/.eslintrc.yaml"; do
    if [[ -f "$cfg" ]]; then
      eslint_config="$cfg"
      break
    fi
  done

  if [[ -n "$eslint_config" ]]; then
    step "sdn-js lint"
    (cd "$ROOT/sdn-js" && npm_config_cache="$ROOT/.npm-cache" npm run lint)
    pass "sdn-js lint"
  else
    echo "Skipping sdn-js lint (no ESLint config found in sdn-js)"
  fi

  # REMOVED: "Upstream IPFS mirror check". It ran
  # scripts/update-upstream-ipfs.sh --check, which since the 2026-07-24 UI
  # clean slate prints "nothing to verify" and exits 0 UNCONDITIONALLY — a
  # green check that checks nothing, which is worse than no check because it
  # reads like coverage. If the mirror subtrees need verifying again, give
  # that script a real --check and put it back.

  step "sdn-js tests"
  # SDN_REQUIRE_GO_INTEROP=1 TURNS THE ONE LIVE INTEROP GATE INTO A GATE.
  #
  # src/go-libp2p-interop.test.ts builds a real go-libp2p host from the PRODUCT
  # Go module and dials it with the browser client — the only thing in this repo
  # that proves an sdn-js release can still talk to an sdn-server node after a
  # libp2p bump. Without this variable it degrades to a console.warn and a
  # describe.skip whenever a Go toolchain is missing, which is exactly the
  # silent-skip blindness the test was written to remove: the suite would stay
  # green through an interop break.
  #
  # This lane already requires Go (every other mode compiles sdn-server), so
  # demanding it here costs nothing and closes that hole.
  (cd "$ROOT/sdn-js" && SDN_REQUIRE_GO_INTEROP=1 npm_config_cache="$ROOT/.npm-cache" npm test -- --run)
  pass "sdn-js test (go-libp2p interop required)"

  step "sdn-js build"
  (cd "$ROOT/sdn-js" && npm_config_cache="$ROOT/.npm-cache" npm run build)
  pass "sdn-js build"

  # BROWSER-EXECUTED DIRECT INTEROP, and it runs on the bundle the step above
  # just produced — dist/index.mjs, the file the npm package ships.
  #
  # The go-libp2p interop test above runs under NODE, over a websocket. A
  # browser can do neither: no TCP, and its unrelayed paths to a node are
  # webtransport and webrtc-direct, both built on APIs Node does not have
  # (WebTransport, RTCPeerConnection) and on certificate-hash authentication
  # only a browser enforces. Node interop passing is no evidence a browser can
  # connect, which is the thing an SDN client has to do.
  #
  # Headless chromium only — never a window on the owner's machine.
  step "sdn-js browser direct interop (webtransport + webrtc-direct)"
  # --with-deps installs system libraries and needs sudo, which a runner has
  # and the owner's machine must never be asked for.
  local playwright_install_args=(install chromium)
  if [[ "${CI:-}" == "true" || "${CI:-}" == "1" ]]; then
    playwright_install_args=(install --with-deps chromium)
  fi
  (cd "$ROOT/sdn-js" \
    && npm_config_cache="$ROOT/.npm-cache" npx --yes playwright "${playwright_install_args[@]}" >/dev/null \
    && SDN_REQUIRE_BROWSER_INTEROP=1 npm_config_cache="$ROOT/.npm-cache" npm run test:browser-interop)
  pass "sdn-js browser direct interop"
}

run_module_delivery_compat() {
  step "module-delivery compatibility deps"
  ensure_npm_deps "$ROOT"
  ensure_npm_deps "$ROOT/sdn-js" eslint vitest tsup
  pass "module-delivery compatibility deps"

  step "module-delivery compatibility"
  (cd "$ROOT" && npm_config_cache="$ROOT/.npm-cache" npm run test:module-delivery)
  pass "module-delivery compatibility"

  # Belongs HERE, not in the preflight: it imports space-data-module-sdk, which
  # is a root dependency, and the preflight installs nothing on purpose. This
  # lane has already run the root install above.
  # Both of these resolve space-data-module-sdk, and this lane is the one that
  # has installed it — module-placement.test.mjs resolves it from sdn-js/
  # rather than the root (createRequire over sdn-js/package.json), which is
  # why hiding only the root node_modules did not reproduce its CI failure.
  step "module placement and catalog seeding"
  (cd "$ROOT" && npm_config_cache="$ROOT/.npm-cache" node --test \
    tests/isomorphic/seed-orbpro-module-catalog.test.mjs \
    scripts/module-placement.test.mjs)
  pass "module placement and catalog seeding"
}

run_plugin_demo() {
  # The demo boots a REAL daemon and gives it 30 seconds to answer
  # /api/node/info (plugin-demo/tests/helpers/test-server.mjs:242). A daemon
  # that starts on a cold AOT cache compiles the engine artifact first —
  # ~49s here, longer on a runner — and would blow that window before serving
  # anything. Prewarm first, and the boot is a few hundred milliseconds.
  prepare_go_toolchain

  step "plugin-demo install"
  ensure_npm_deps "$ROOT/plugin-demo/tests"
  pass "plugin-demo npm install"

  # Pre-build the test binary with the correct CGO flags so test-server.mjs
  # finds an already-built binary and skips the rebuild step.
  step "plugin-demo pre-build server binary"
  (cd "$ROOT/sdn-server" && "$ROOT/scripts/go-with-wasmedge.sh" build -o spacedatanetwork-test ./cmd/spacedatanetwork)
  pass "plugin-demo pre-build server binary"

  step "plugin-demo integration tests"
  node "$ROOT/plugin-demo/tests/integration.test.mjs"
  pass "plugin-demo integration tests"
}
run_encryption() {
  if [[ ! -d "$ROOT/tests/encryption/go" ]]; then
    echo "Encryption tests directory missing, skipping"
    return
  fi

  step "Encryption tests (Go)"
  (cd "$ROOT/tests/encryption/go" && GOCACHE="$ROOT/.gocache" go test -race -count=1 ./...)
  pass "encryption go tests"
}

case "$MODE" in
  quick)
    run_preflight
    # run_kubo_pin stays in quick: it is the ONLY caller, and the split below
    # enumerates leaf modes, so dropping it here would silently retire the
    # shipped-Kubo pin gate the moment this landed.
    run_kubo_pin
    run_encryption
    run_go # runs gofmt too, which is why `quick` does not repeat it
    run_go_builds
    run_sdn_js
    run_module_delivery_compat
    run_plugin_demo
    ;;
  full|all)
    run_preflight
    run_kubo_pin
    run_go
    run_go_heavy
    run_go_race
    run_go_builds
    run_sdn_js
    run_module_delivery_compat
    run_plugin_demo
    run_encryption
    ;;
  preflight)
    run_preflight
    run_gofmt
    run_docs_release
    ;;
  go)
    run_go
    ;;
  heavy|go-heavy)
    run_go_heavy
    ;;
  builds|go-builds)
    run_go_builds
    ;;
  race)
    run_go_race
    ;;
  js)
    run_sdn_js
    ;;
  kubo-pin|versions)
    run_kubo_pin
    ;;
  delivery|module-delivery|plugin)
    run_module_delivery_compat
    ;;
  demo|plugin-demo)
    run_plugin_demo
    ;;
  encryption)
    run_encryption
    ;;
  *)
    echo -e "${RED}Usage: $0 [quick|full|preflight|go|heavy|builds|race|js|kubo-pin|delivery|plugin|demo|encryption]${NC}"
    exit 1
    ;;
esac

echo -e "\n${GREEN}CI PASSED (${MODE})${NC}"
