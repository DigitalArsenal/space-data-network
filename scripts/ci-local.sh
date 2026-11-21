#!/usr/bin/env bash
# Canonical local CI runner.
# This script is intentionally aligned with .github/workflows/ci.yml.
#
# ONE SCRIPT, TWO CALLERS. A developer runs `quick` (opt-in at pre-push with
# SDN_PREPUSH_CI=1; the default pre-push hook runs only the secret and
# mnemonic scans); CI runs the same modes, one per job. Splitting CI into
# parallel jobs is a job-shape change only — every check still lives here,
# and no check lives in the workflow file.
#
#   .github/workflows/ci.yml job   modes it runs
#   ----------------------------   ---------------------------
#   preflight                      preflight, encryption
#   go-quick (four package groups) go
#   go-heavy (eleven test groups)  heavy
#   publication-e2e (three groups) publication
#   go-builds                      builds
#   js                             js
#   delivery                       delivery
#   demo                           demo
#   wasmedge-static                prefix-pack
#   wasmedge-runtime               runtime
#   wasmedge-substrate             migrations
#   update-channel-e2e (two groups) update-channel
#   dashboard                      dashboard
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
#   ./scripts/ci-local.sh heavy      # the heavy Go packages (internal/storage) with the budget they need; SDN_CI_STORAGE_GROUP=<group> runs one group
#   ./scripts/ci-local.sh full       # quick + heavy + race
#   ./scripts/ci-local.sh preflight  # OSS preflight + gofmt only (no toolchain needed)
#   ./scripts/ci-local.sh go         # fast go checks only
#   ./scripts/ci-local.sh builds     # node + edge-relay builds only
#   ./scripts/ci-local.sh race       # CI-only/full race suite
#   ./scripts/ci-local.sh js         # sdn-js checks only
#   ./scripts/ci-local.sh dashboard  # embedded dashboard against the built node
#   ./scripts/ci-local.sh kubo-pin   # the linked Kubo: version and directives only
#   ./scripts/ci-local.sh delivery   # focused module-delivery compatibility checks
#   ./scripts/ci-local.sh plugin     # legacy alias for delivery
#   ./scripts/ci-local.sh demo       # plugin-demo integration tests only
#   ./scripts/ci-local.sh publication # publication between two real daemons
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
    scripts/run-govulncheck.test.mjs \
    scripts/check-no-orphan-test-suites.test.mjs \
    scripts/prepush-hook.test.mjs \
    scripts/ci-heavy-coverage.test.mjs)
  pass "dependency-drift check suites"

  # Release-assembly logic, which decides what a published release CONTAINS.
  # assemble-beta-release-artifacts.test.mjs was run by no script and no
  # workflow; a test nothing runs is a comment. Both suites are pure Node over
  # temp directories — no toolchain, no network — so they belong in the
  # cheapest gate there is.
  step "Release artifact check suites"
  (cd "$ROOT" && node --test \
    deployment/release/assemble-beta-release-artifacts.test.mjs \
    scripts/merge-mac-update-feed.test.mjs \
    scripts/sync-docs-release.test.mjs)
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
    deployment/release/beta-release-workflow.test.mjs \
    deployment/release/build-cli-update-payload.test.mjs \
    deployment/release/build-self-contained-cli.test.mjs \
    deployment/release/build-update-carrier.test.mjs \
    deployment/release/cli-update-payload-trust.test.mjs \
    deployment/release/extract-release-binary.test.mjs \
    deployment/release/fresh-key-password.test.mjs \
    deployment/release/install-script.test.mjs \
    deployment/release/live-dht-client-smoke.test.mjs \
    deployment/release/live-dht-summary.test.mjs \
    deployment/release/live-dht-workflow.test.mjs \
    deployment/release/prepare-beta-release.test.mjs \
    deployment/release/publish-fleet-update.test.mjs \
    deployment/release/published-install-smoke.test.mjs \
    deployment/release/sign-update-manifest.test.mjs \
    deployment/release/test-release-artifacts-docker.test.mjs \
    deployment/release/update-channel-docs.test.mjs \
    deployment/release/update-manifest-statement-domain.test.mjs \
    deployment/release/update-trust-roots.test.mjs \
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

# The Kubo linked into the node: its version everywhere it is named, and
# Kubo's own replace/exclude directives in sdn-server/go.mod. Cheap and
# offline. The module-by-module lockstep with Kubo's build needs the module
# proxy, so the security lane runs it (check-kubo-pin.js --build-list).
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
  step "Kubo linked into SDN"
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
  if [[ "${SDN_CI_GO_COVERAGE:-0}" == 1 ]]; then
    local arg skip='a^' run='.'
    for arg in "$@"; do
      case "$arg" in
        -skip=*) skip="${arg#-skip=}" ;;
        -run=*) run="${arg#-run=}" ;;
      esac
    done
    "$ROOT/scripts/go-with-wasmedge.sh" test -list . "$@" > "$log.list" 2>&1 || { cat "$log.list"; return 1; }
    awk '/^(Test|Example|Fuzz)[[:alnum:]_]+$/ {print}' "$log.list" | grep -E "$run" | grep -Ev "$skip" | LC_ALL=C sort > "$log.expected" || true
  fi
  set +e
  "$ROOT/scripts/go-with-wasmedge.sh" test "$@" 2>&1 | tee "$log"
  rc=${PIPESTATUS[0]}
  set -e
  report_pkg_timings "$label" "$log"
  if [[ "${SDN_CI_GO_COVERAGE:-0}" == 1 ]]; then
    awk '$1 == "===" && $2 == "RUN" && $3 !~ /\// && $0 !~ /^[[:space:]]/ {print $3}' "$log" | LC_ALL=C sort > "$log.ran"
    awk '$1 == "---" && ($2 == "PASS:" || $2 == "FAIL:" || $2 == "SKIP:") && $3 !~ /\// && $0 !~ /^[[:space:]]/ {print $3}' "$log" | LC_ALL=C sort > "$log.ended"
    for arg in ran ended; do
      if ! cmp -s "$log.expected" "$log.$arg"; then
        echo "Go test coverage mismatch ($label, $arg):"
        diff -u "$log.expected" "$log.$arg" || true
        rc=1
      fi
    done
    [[ "$rc" != 0 ]] || echo "Go test coverage: $label ran and completed every listed test exactly once"
  fi
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

# These packages retain the budgets used by their patched-runtime suites.
EXTENDED_GO_PACKAGES="github.com/spacedatanetwork/sdn-server/internal/flatsqlrt github.com/spacedatanetwork/sdn-server/internal/storage/format2 github.com/spacedatanetwork/sdn-server/internal/storage/format4 github.com/spacedatanetwork/sdn-server/internal/wasmrt github.com/spacedatanetwork/sdn-server/cmd/spacedatanetwork"
MIGRATION_GO_TESTS="^(TestFormat2|TestStoreMigrate)"
UPDATE_GO_TESTS="^TestUpdateChannelRealDaemonE2E$"

heavy_pkg_filter() {
  local filter=""
  for pkg in $HEAVY_GO_PACKAGES $EXTENDED_GO_PACKAGES; do
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
# The 2026-11-16 Linux run spent 16m38s in this test before its group
# reached 25m; the 100,000-object case was still running after 9m34s.
# Alone on macOS with patched WasmEdge, both cases passed in 214.28s.
# Give the complete scale exercise its own budget, in a group of its own.
HEAVY_GO_LONG_TESTS="TestPublicationCatalogScaleDaily"
HEAVY_GO_LONG_TEST_TIMEOUT="60m"
# The record store's other tests, in groups by test name: name:pattern, the
# pattern an extended regular expression over the test name. A test belongs
# to the one group whose pattern matches it; group `rest` holds every test no
# pattern matches, so a new test always lands in a group. A test that two
# patterns match fails the lane. SDN_CI_STORAGE_GROUP selects one group (one
# CI job each: long, each name below, rest); unset runs all of them, at most
# HEAVY_GO_PARALLEL at a time.
#
# Sized from the record store runs of 2026-11-18: 44340523082 passed with
# 3,397s of test time; 44379302576 and 44379081909 reached the 25m panic with
# the same tests about 1.6 times slower (four groups shared one runner). The
# 20 tests that call onFormat4Engine take 100 to 120s each in the passing run
# and 165 to 191s in the slow ones. On the slow figures every group below is
# 480 to 575s, and `long` 101s.
HEAVY_GO_GROUPS="format4a:^TestFormat4[A-L] format4b:^TestFormat4([M-Q]|Ra) format4c:^TestFormat4R[b-z] format4d:^TestFormat4S format4e:^TestFormat4U format4f:^Test(Format4[V-Z]|Asset) format5:^Test(Format5|Unified) format2:^Test(Format2|Unlimited) publication:^Test(Publication|Sealed)"
HEAVY_GO_PARALLEL=4
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

# Package ownership is exhaustive: new packages enter the general group,
# whose sorted packages alternate between general-1 and general-2 (run
# 43979527185: 568s of tests but 23m in the step, most of it linking 84
# test binaries one at a time).
# The other groups isolate the measured 1220s format2, 575s command, and
# 374s engine packages. Every group retains each package's test budget.
# The format2-kill group runs format2's kill-recovery test alone (1097s and
# 1579s in runs 43938905898 and 43971615013, of the package's 1676s); the
# format2 group runs the rest of that package.
FORMAT2_KILL_GO_TEST="^TestRouterKill9KeepsEveryAckedRecord$"

quick_go_group() {
  case "$1" in
    */internal/storage/format2) echo format2 ;;
    */cmd/spacedatanetwork) echo command ;;
    */internal/flatsqlrt|*/internal/storage/format4|*/internal/wasmrt) echo engine ;;
    *) echo general ;;
  esac
}

run_go() {
  prepare_go_toolchain
  run_gofmt
  export SDN_WASM_ATOMIC_REGRESSION=1
  export SDN_CI_GO_COVERAGE=1
  local group="${SDN_CI_GO_GROUP:-all}"
  case "$group" in all|general|general-1|general-2|format2|format2-kill|command|engine) ;; *) fail "unknown Go group: $group" ;; esac

  step "Go tests (quick: $group)"
  local pkgs failed=0 pkg
  pkgs=$("$ROOT/scripts/go-with-wasmedge.sh" list ./... | grep -Ev "$(heavy_pkg_filter)")
  local manifest="$ROOT/.ci-artifacts/quick-packages.txt"
  mkdir -p "$ROOT/.ci-artifacts"
  for pkg in $pkgs $EXTENDED_GO_PACKAGES; do
    printf '%s %s\n' "$pkg" "$(quick_go_group "$pkg")"
  done | LC_ALL=C sort > "$manifest"
  "$ROOT/scripts/go-with-wasmedge.sh" list ./... | grep -Fxv -e "$HEAVY_GO_PACKAGES" | LC_ALL=C sort > "$manifest.expected"
  cut -d ' ' -f 1 "$manifest" > "$manifest.actual"
  cmp -s "$manifest.expected" "$manifest.actual" || fail "quick package ownership differs from go list"
  [[ -z "$(uniq -d "$manifest.actual")" ]] || fail "duplicate quick package ownership"
  case "$group" in
    general-1) pkgs=$(printf '%s\n' $pkgs | LC_ALL=C sort | awk 'NR % 2 == 1') ;;
    general-2) pkgs=$(printf '%s\n' $pkgs | LC_ALL=C sort | awk 'NR % 2 == 0') ;;
  esac
  if [[ "$group" == all || "$group" == general* ]]; then
    # shellcheck disable=SC2086
    go_test_with_timings "quick-$group" -v -p=1 -timeout="$QUICK_GO_TEST_TIMEOUT" -count=1 $pkgs \
      || failed=1
  fi
  run_go_extended || failed=1
  unset SDN_CI_GO_COVERAGE
  [[ "$failed" == 0 ]] || fail "go test (quick: $group)"
  pass "go test (quick: $group)"
}

run_go_extended() {
  local pkg budget label failed=0
  local group="${SDN_CI_GO_GROUP:-all}"
  local skip=()
  for pkg in $EXTENDED_GO_PACKAGES; do
    skip=()
    label="quick-${pkg##*/}"
    case "$group" in
      all) ;;
      format2-kill)
        [[ "$(quick_go_group "$pkg")" == format2 ]] || continue
        skip=(-run="$FORMAT2_KILL_GO_TEST")
        label="quick-format2-kill"
        ;;
      format2)
        [[ "$(quick_go_group "$pkg")" == format2 ]] || continue
        skip=(-skip="$FORMAT2_KILL_GO_TEST")
        ;;
      *) [[ "$(quick_go_group "$pkg")" == "$group" ]] || continue ;;
    esac
    budget=20m
    case "$pkg" in
      */flatsqlrt|*/format2) budget=30m ;;
      */cmd/spacedatanetwork) skip=(-skip="${MIGRATION_GO_TESTS}|${UPDATE_GO_TESTS}") ;;
    esac
    # SDN_CI_KEEP_TEST_BINARIES=1 keeps each test binary in
    # .ci-artifacts/testbin, so a native crash's pc can be symbolized
    # against the exact binary that crashed (stack issue 876).
    local keep=()
    if [[ "${SDN_CI_KEEP_TEST_BINARIES:-0}" == 1 ]]; then
      mkdir -p "$ROOT/.ci-artifacts/testbin"
      keep=(-o "$ROOT/.ci-artifacts/testbin/")
    fi
    go_test_with_timings "$label" -v -p=1 -timeout="$budget" -count=1 ${keep[@]+"${keep[@]}"} ${skip[@]+"${skip[@]}"} "$pkg" \
      || failed=1
  done
  return "$failed"
}

# Publication between real daemons, in three groups by test name. Run
# 44031044072 ran the whole suite in one binary and reached go test's 50m
# panic during its tenth test; its nine finished tests took 543s (Retention),
# 473s (Follow), 356s (the two-daemon delivery test), 297s (Fingerprint),
# 260s (LaneCount), 225s (FollowStatus), 223s (ImportChunks), 220s
# (InvalidSignature) and 213s (Key); ContentAddress takes about as long as
# Key (72.7s against 67.9s on macOS). Each group is about 1,000s of runner
# time against a 45m budget. Run 44564628985 took 160s to 540s per test
# (about 230s for most), and two groups of ten and eleven tests reached the
# 45m budget; the manual-run and publication-alert tests are publication-5
# and publication-6. Every group checks that the six lists together
# are exactly the package's ^TestPublication.*RealDaemons$ tests, each listed
# once, and that each test of its own list ran and ended exactly once.
PUBLICATION_GO_TESTS="^TestPublication.*RealDaemons$"
PUBLICATION_GO_TEST_TIMEOUT="45m"

publication_group_tests() {
  case "$1" in
    publication-1) echo "TestPublicationRetentionRealDaemons TestPublicationFollowRealDaemons TestPublicationOutlivesClientRealDaemons TestPublicationGossipGateRealDaemons TestPublicationPNMOnlyLaneRealDaemons TestPublicationUnheldBatchImportsPNMRealDaemons TestPublicationStorePanicReleasesReservationRealDaemons" ;;
    publication-2) echo "TestPublicationRealDaemons TestPublicationFingerprintRealDaemons TestPublicationContentAddressRealDaemons TestPublicationKeyRealDaemons TestPublicationSlowFlowRealDaemons TestPublicationSubscriptionFileOrderRealDaemons TestPublicationPNMGuardRealDaemons" ;;
    publication-3) echo "TestPublicationLaneCountRealDaemons TestPublicationFollowStatusRealDaemons TestPublicationImportChunksRealDaemons TestPublicationInvalidSignatureRealDaemons TestPublicationFailureRealDaemons TestPublicationCursorRefusedRealDaemons TestPublicationStreamSlotsRealDaemons TestPublicationSubscriptionsUnreadableRealDaemons TestPublicationSubscriptionUnknownFieldRealDaemons" ;;
    publication-4) echo "TestPublicationSubscribeRealDaemons TestPublicationUpgradeRealDaemons TestPublicationRollbackRealDaemons TestPublicationConversionRecordRealDaemons TestPublicationDiskFloorRealDaemons TestPublicationRestartStreamedPNMRealDaemons" ;;
    publication-5) echo "TestPublicationManualRunOutlivesClientRealDaemons TestPublicationManualRunCancelByIDRealDaemons TestPublicationManualRunStopsWithNodeRealDaemons TestPublicationManualRunBoundRealDaemons TestPublicationAlertRestartRealDaemons TestPublicationAlertFailureDuringClearRealDaemons" ;;
    publication-6) echo "TestPublicationAlertGainedAfterFailureRealDaemons TestPublicationAlertClearsWhenLaterRunsPublishItRealDaemons TestPublicationAlertPartialFailureRealDaemons TestPublicationAlertBatchGainedRecordsRealDaemons TestPublicationAlertHeadPublicationRealDaemons TestPublicationAlertSupersededBatchRealDaemons" ;;
  esac
}

# The 1.1.9 release binary TestPublicationUpgradeRealDaemons updates from:
# the published archive, checked against its release checksum, and its
# daemon binary (runtime/sdn/spacedatanetwork; bin/ holds a launcher script).
fetch_v119_binary() {
  [[ -n "${SDN_E2E_V119_BINARY:-}" ]] && return 0
  local asset sum dir
  case "$(uname -s)-$(uname -m)" in
    Linux-x86_64) asset=spacedatanetwork-1.1.9-linux-amd64.tar.gz sum=45127438512c09be90a26991c8463401e596af89eae98a66da96c95c1f253e07 ;;
    Linux-aarch64) asset=spacedatanetwork-1.1.9-linux-arm64.tar.gz sum=b585921e0eca17670833291eec6aba3969160a72eb796cf255553e7ae08cc516 ;;
    Darwin-arm64) asset=spacedatanetwork-1.1.9-darwin-arm64.tar.gz sum=372b7601593a7a1950fd3de14e0d5697194683c1394b95874eef0905cd36f748 ;;
    *) fail "no 1.1.9 release archive for $(uname -s)-$(uname -m); set SDN_E2E_V119_BINARY to a build of tag v1.1.9" ;;
  esac
  dir="$ROOT/.ci-artifacts/v1.1.9"
  mkdir -p "$dir"
  [[ -f "$dir/$asset" ]] || curl --fail --location --retry 3 -o "$dir/$asset" \
    "https://github.com/DigitalArsenal/space-data-network/releases/download/v1.1.9/$asset" || fail "download $asset"
  [[ "$( (sha256sum "$dir/$asset" 2>/dev/null || shasum -a 256 "$dir/$asset") | cut -d' ' -f1)" == "$sum" ]] \
    || fail "$asset does not match its release checksum"
  # GNU tar matches a member pattern only with --wildcards; bsdtar always does.
  local wild=()
  case "$(tar --version 2>/dev/null)" in *'GNU tar'*) wild=(--wildcards) ;; esac
  tar -xzf "$dir/$asset" -C "$dir" --strip-components=3 ${wild[@]+"${wild[@]}"} '*/runtime/sdn/spacedatanetwork' || fail "extract the 1.1.9 daemon"
  export SDN_E2E_V119_BINARY="$dir/spacedatanetwork"
}

run_publication() {
  local group="${SDN_CI_PUBLICATION_GROUP:-all}" run g
  case "$group" in all|publication-1|publication-2|publication-3|publication-4|publication-5|publication-6) ;; *) fail "unknown publication group: $group" ;; esac
  step "Publication between real daemons ($group)"
  local list="$ROOT/.ci-artifacts/publication-tests"
  mkdir -p "$ROOT/.ci-artifacts"
  "$ROOT/scripts/go-with-wasmedge.sh" test -list "$PUBLICATION_GO_TESTS" ./cmd/spacedatanetwork/ > "$list.go" 2>&1 \
    || { cat "$list.go"; fail "listing publication tests"; }
  grep -E "$PUBLICATION_GO_TESTS" "$list.go" | LC_ALL=C sort > "$list.expected" || true
  for g in publication-1 publication-2 publication-3 publication-4 publication-5 publication-6; do
    printf '%s\n' $(publication_group_tests "$g")
  done | LC_ALL=C sort > "$list.grouped"
  [[ -s "$list.expected" ]] || fail "no publication tests listed"
  [[ -z "$(uniq -d "$list.grouped")" ]] || fail "a publication test is in more than one group"
  cmp -s "$list.expected" "$list.grouped" || { diff -u "$list.expected" "$list.grouped" || true; fail "publication groups differ from the package's tests"; }
  local budget="$PUBLICATION_GO_TEST_TIMEOUT"
  if [[ "$group" == all ]]; then
    run="$PUBLICATION_GO_TESTS"
    budget=60m
  else
    run="^($(publication_group_tests "$group" | tr ' ' '|'))\$"
  fi
  case "$group" in all|publication-4) fetch_v119_binary ;; esac
  export SDN_E2E_PUBLICATION=1
  export SDN_CI_GO_COVERAGE=1
  go_test_with_timings "publication-${group#publication-}" -v -p=1 -timeout="$budget" -count=1 -run="$run" ./cmd/spacedatanetwork/ \
    || fail "publication real daemons ($group)"
  unset SDN_CI_GO_COVERAGE
  pass "publication real daemons ($group)"
}

run_prefix_build() {
  . "$ROOT/scripts/wasmedge-static-env.sh"
  WASMEDGE_STATIC_WORK="$ROOT/.sdn-wasmedge-static" WASMEDGE_STATIC_PREFIX_ONLY=1 \
    bash "$ROOT/scripts/build-static-wasmedge.sh"
}

run_prefix_pack() {
  mkdir -p "$ROOT/.ci-artifacts"
  test -s "$ROOT/.sdn-wasmedge-static/prefix/link.flags"
  tar -czf "$ROOT/.ci-artifacts/wasmedge-static-prefix.tar.gz" -C "$ROOT/.sdn-wasmedge-static" prefix
}

run_prefix_unpack() {
  mkdir -p "$ROOT/.sdn-wasmedge-static"
  tar -xzf "$ROOT/.ci-artifacts/wasmedge-static-prefix.tar.gz" -C "$ROOT/.sdn-wasmedge-static"
  test -s "$ROOT/.sdn-wasmedge-static/prefix/link.flags"
  test -s "$ROOT/.sdn-wasmedge-static/prefix/sdn-runtime-patches.txt"
}

run_runtime() {
  prepare_go_toolchain
  "$ROOT/.ci-artifacts/spacedatanetwork-prewarm" substrate-selftest --require-patched
}

run_migrations() {
  prepare_go_toolchain
  go_test_with_timings migrations -v -p=1 -timeout=60m -count=1 -run "$MIGRATION_GO_TESTS" ./cmd/spacedatanetwork/ \
    || fail "store migrations"
}

run_update_channel() {
  prepare_go_toolchain no-aot
  export SDN_E2E_REAL_DAEMON=1
  if [[ -n "${SDN_E2E_UPDATE_USER:-}" ]]; then
    run_update_channel_isolated
    return
  fi
  local select
  check_update_group
  read -r -a select <<< "$(update_group_args go)"
  go_test_with_timings update-channel -v -p=1 -timeout=50m -count=1 "${select[@]}" ./cmd/spacedatanetwork/ \
    || fail "update channel"
}

# SDN_CI_UPDATE_GROUP splits TestUpdateChannelRealDaemonE2E into four CI
# jobs: `home` runs its home-read-only-install subtests, `fleet` the published
# receiver, rollout, fleet view and --config subtests, `rollout` the update
# status guard, the rollout's signal paths and its races, `channel` all the
# others; unset runs all of them. Run 44245748601 ran everything in one binary and hit go
# test's 50m panic (the home subtests alone take about 15m on a runner); run
# 44379302576's channel group took 42.5m of its 50m, and run 44408129190's
# fleet group 24.5m of its job.
# update_group_args go|test prints the selection as `go test` or test-binary
# flags.
update_group_args() {
  local p="-"
  [[ "$1" == test ]] && p="-test."
  local home="${UPDATE_GO_TESTS}/^home-read-only-install\$"
  local fleet="${UPDATE_GO_TESTS}/^(installed-v1.1.9-push|rollout-fleet|config-flag-tls)\$"
  local rollout="${UPDATE_GO_TESTS}/^(fleet-status-guard|rollout-signals|rollout-races)\$"
  local others="${UPDATE_GO_TESTS}/^(home-read-only-install|installed-v1.1.9-push|rollout-fleet|config-flag-tls|fleet-status-guard|rollout-signals|rollout-races)\$"
  case "${SDN_CI_UPDATE_GROUP:-all}" in
    home) printf '%s\n' "${p}run=${home}" ;;
    fleet) printf '%s\n' "${p}run=${fleet}" ;;
    rollout) printf '%s\n' "${p}run=${rollout}" ;;
    channel) printf '%s\n' "${p}run=${UPDATE_GO_TESTS} ${p}skip=${others}" ;;
    *) printf '%s\n' "${p}run=${UPDATE_GO_TESTS}" ;;
  esac
}

check_update_group() {
  case "${SDN_CI_UPDATE_GROUP:-all}" in
    home|fleet|rollout|channel|all) ;;
    *) fail "unknown SDN_CI_UPDATE_GROUP=${SDN_CI_UPDATE_GROUP} (home, fleet, rollout, channel or unset)" ;;
  esac
}

# With SDN_E2E_UPDATE_USER (CI, as root on Linux): the fixtures and the test
# binary are built first, then the test runs as that ordinary user in a network
# namespace whose only interface is loopback.
run_update_channel_isolated() {
  local user="$SDN_E2E_UPDATE_USER"
  local bin="${RUNNER_TEMP:-$ROOT/.ci-artifacts}/update-channel-bin"
  local logs="${SDN_E2E_LOG_DIR:?SDN_E2E_LOG_DIR names the log directory}"
  local build select
  check_update_group
  read -r -a select <<< "$(update_group_args test)"
  step "Update channel fixtures (v1, v2, v3 never ready, v6 and v7 never open their store, v8 killed mid-install, v11 exits at start, v15 killed mid-pass, v16 whose update clock moves back, the release signal signer)"
  mkdir -p "$bin" "$logs"
  for build in v1 v2 v3 v6 v7 v8 v11 v15 v16; do
    local build_args=(-o "$bin/spacedatanetwork-$build")
    case "$build" in
      v3) build_args+=(-tags sdn_e2e_not_ready) ;;
      v6) build_args+=(-tags sdn_e2e_hang_before_store) ;;
      v7) build_args+=(-tags sdn_e2e_hang_before_store,sdn_e2e_no_trial_timer) ;;
      v8) build_args+=(-tags sdn_e2e_crash_mid_apply) ;;
      v11) build_args+=(-tags sdn_e2e_crash_at_start) ;;
      v15) build_args+=(-tags sdn_e2e_crash_mid_pass) ;;
      v16) build_args+=(-tags sdn_e2e_clock_back) ;;
    esac
    (cd "$ROOT/sdn-server" && SDN_GO_LDFLAGS="-X github.com/spacedatanetwork/sdn-server/internal/versioninfo.ReleaseTag=e2e-$build" \
      ../scripts/go-with-wasmedge.sh build "${build_args[@]}" ./cmd/spacedatanetwork) || fail "build update fixture $build"
  done
  # The release workflow's signal signer, built the way that workflow builds it.
  (cd "$ROOT/sdn-server" && CGO_ENABLED=0 go build -trimpath -o "$bin/sdn-update-signal-signer" ./cmd/sdn-update-signal-signer) \
    || fail "build the release signal signer"
  (cd "$ROOT/sdn-server" && ../scripts/go-with-wasmedge.sh test -c -o "$bin/update-channel-e2e.test" ./cmd/spacedatanetwork) \
    || fail "build the update channel test"
  pass "update channel fixtures"

  step "Update channel E2E (${SDN_CI_UPDATE_GROUP:-all}) as $user, loopback only"
  chmod 0777 "$logs"
  chmod a+x "$(dirname "$logs")"
  chmod -R a+rX "$bin" "$WASMEDGE_DIR"
  if [[ -n "${RUNNER_TEMP:-}" ]]; then chmod a+x "$RUNNER_TEMP"; fi
  if [[ -n "${SDN_E2E_INSTALLED_BUNDLE:-}" && -f "$SDN_E2E_INSTALLED_BUNDLE" ]]; then
    chmod a+r "$SDN_E2E_INSTALLED_BUNDLE"
    chmod a+x "$(dirname "$SDN_E2E_INSTALLED_BUNDLE")"
  fi
  (cd "$ROOT/sdn-server/cmd/spacedatanetwork" && env SDN_E2E_REAL_DAEMON=1 HOME="$(getent passwd "$user" | cut -d: -f6)" \
    SDN_E2E_REAL_DAEMON_BIN_DIR="$bin" \
    SDN_E2E_LOG_DIR="$logs" \
    SDN_E2E_INSTALLED_BUNDLE="${SDN_E2E_INSTALLED_BUNDLE:-}" \
    LD_LIBRARY_PATH="$WASMEDGE_DIR/lib:$WASMEDGE_DIR/lib64" \
    unshare --net -- bash -c 'set -e; ip link set lo up; exec setpriv --reuid="$1" --regid="$2" --init-groups "$3" -test.v -test.timeout=50m "${@:4}"' \
    -- "$(id -u "$user")" "$(id -g "$user")" "$bin/update-channel-e2e.test" "${select[@]}" \
    | tee "$logs/test.log") || fail "update channel"
  pass "update channel"
}

# heavy_group_names prints every record store group: long, the named groups
# of HEAVY_GO_GROUPS in order, then rest.
heavy_group_names() {
  local defs=() def names="long"
  read -r -a defs <<< "$HEAVY_GO_GROUPS"
  for def in "${defs[@]}"; do
    names="$names ${def%%:*}"
  done
  echo "$names rest"
}

run_go_heavy() {
  local selected="${SDN_CI_STORAGE_GROUP:-all}" names
  names="$(heavy_group_names)"
  [[ "$selected" == all || " $names " == *" $selected "* ]] \
    || fail "unknown SDN_CI_STORAGE_GROUP=$selected ($names, or unset)"
  prepare_go_toolchain

  step "Go tests (record store, group ${selected}: $HEAVY_GO_PACKAGES)"
  local pkg group pattern pid failed listed def
  local artifact_dir="$ROOT/.ci-artifacts"
  local pids=()
  local logs=()
  local defs=()
  read -r -a defs <<< "$HEAVY_GO_GROUPS"
  mkdir -p "$artifact_dir"
  for pkg in $HEAVY_GO_PACKAGES; do
    local label="${pkg##*/}"
    listed="$artifact_dir/heavy-${label}-listed.txt"
    "$ROOT/scripts/go-with-wasmedge.sh" test -list '^(Test|Example|Fuzz)' "$pkg" > "$listed" \
      || fail "list tests in $pkg"
    if LC_ALL=C grep -E '^(Test|Example|Fuzz)' "$listed" | LC_ALL=C grep -Ev '^(Test|Example|Fuzz)[A-Za-z0-9_]*$'; then
      fail "a discovered test name cannot be represented by the group filter"
    fi
    LC_ALL=C grep -E '^(Test|Example|Fuzz)[A-Za-z0-9_]*$' "$listed" | LC_ALL=C sort > "$artifact_dir/heavy-${label}-all.txt"
    [[ -s "$artifact_dir/heavy-${label}-all.txt" ]] || fail "no tests discovered in $pkg"
    # Split named long tests before assigning the remaining names. Every job
    # assigns every discovered test, so each checks the whole partition.
    # Discovery remains authoritative, including examples and fuzz seeds.
    printf '%s\n' $HEAVY_GO_LONG_TESTS | LC_ALL=C sort > "$artifact_dir/heavy-${label}-long.txt"
    grep -Fxv -f "$artifact_dir/heavy-${label}-long.txt" "$artifact_dir/heavy-${label}-all.txt" > "$artifact_dir/heavy-${label}-regular.txt" || [[ $? == 1 ]]
    : > "$artifact_dir/heavy-${label}-claimed.txt"
    for def in "${defs[@]}"; do
      group="${def%%:*}"
      LC_ALL=C grep -E "${def#*:}" "$artifact_dir/heavy-${label}-regular.txt" > "$artifact_dir/heavy-${label}-${group}.txt" || [[ $? == 1 ]]
      cat "$artifact_dir/heavy-${label}-${group}.txt" >> "$artifact_dir/heavy-${label}-claimed.txt"
    done
    [[ -z "$(LC_ALL=C sort "$artifact_dir/heavy-${label}-claimed.txt" | uniq -d)" ]] \
      || fail "a test matches more than one group pattern in HEAVY_GO_GROUPS"
    grep -Fxv -f "$artifact_dir/heavy-${label}-claimed.txt" "$artifact_dir/heavy-${label}-regular.txt" > "$artifact_dir/heavy-${label}-rest.txt" || [[ $? == 1 ]]
    for group in $names; do
      cat "$artifact_dir/heavy-${label}-${group}.txt"
    done | LC_ALL=C sort > "$artifact_dir/heavy-${label}-covered.txt"
    cmp -s "$artifact_dir/heavy-${label}-all.txt" "$artifact_dir/heavy-${label}-covered.txt" \
      || fail "test group coverage differs from go test -list"
    [[ -z "$(uniq -d "$artifact_dir/heavy-${label}-covered.txt")" ]] \
      || fail "a test appears more than once"
    for group in $names; do
      echo "Group $group: $(wc -l < "$artifact_dir/heavy-${label}-${group}.txt" | tr -d ' ') tests"
    done
    failed=0
    logs=()
    : > "$artifact_dir/heavy-${label}-selected.txt"
    if [[ "$selected" == all || "$selected" == long ]]; then
      cat "$artifact_dir/heavy-${label}-long.txt" >> "$artifact_dir/heavy-${label}-selected.txt"
      logs+=("$artifact_dir/gotest-heavy-${label}-long.log")
      pattern="^($(paste -sd '|' "$artifact_dir/heavy-${label}-long.txt"))$"
      go_test_with_timings "heavy-${label}-long" -v -p=1 -timeout="$HEAVY_GO_LONG_TEST_TIMEOUT" -count=1 -run "$pattern" "$pkg" || failed=1
    fi
    pids=()
    for group in $names; do
      [[ "$group" != long ]] || continue
      [[ "$selected" == all || "$selected" == "$group" ]] || continue
      cat "$artifact_dir/heavy-${label}-${group}.txt" >> "$artifact_dir/heavy-${label}-selected.txt"
      [[ -s "$artifact_dir/heavy-${label}-${group}.txt" ]] || continue
      pattern="^($(paste -sd '|' "$artifact_dir/heavy-${label}-${group}.txt"))$"
      logs+=("$artifact_dir/gotest-heavy-${label}-${group}.log")
      go_test_with_timings "heavy-${label}-${group}" -v -p=1 -timeout="$HEAVY_GO_TEST_TIMEOUT" -count=1 -run "$pattern" "$pkg" &
      pids+=("$!")
      if (( ${#pids[@]} >= HEAVY_GO_PARALLEL )); then
        for pid in "${pids[@]}"; do
          wait "$pid" || failed=1
        done
        pids=()
      fi
    done
    for pid in ${pids[@]+"${pids[@]}"}; do
      wait "$pid" || failed=1
    done
    # Validate execution, independently of the planned group manifests.
    local ran="$artifact_dir/heavy-${label}-ran.txt"
    local ended="$artifact_dir/heavy-${label}-ended.txt"
    LC_ALL=C sort -o "$artifact_dir/heavy-${label}-selected.txt" "$artifact_dir/heavy-${label}-selected.txt"
    awk '$1 == "===" && $2 == "RUN" && $3 !~ /\// { print $3 }' \
      ${logs[@]+"${logs[@]}"} /dev/null | LC_ALL=C sort > "$ran"
    awk '$1 == "---" && ($2 == "PASS:" || $2 == "FAIL:" || $2 == "SKIP:") && $3 !~ /\// { print $3 }' \
      ${logs[@]+"${logs[@]}"} /dev/null | LC_ALL=C sort > "$ended"
    cmp -s "$artifact_dir/heavy-${label}-selected.txt" "$ran" \
      || fail "executed tests differ from the group's tests (missing, extra, or duplicate execution)"
    cmp -s "$artifact_dir/heavy-${label}-selected.txt" "$ended" \
      || fail "completed tests differ from the group's tests"
    [[ "$failed" == 0 ]] || fail "go test (heavy: $selected)"
  done
  pass "go test (heavy: $selected)"
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

prepare_sdn_js_deps() {
  step "sdn-js install"
  ensure_npm_deps "$ROOT/sdn-js" eslint vitest tsup
  pass "sdn-js npm ci"
}

# Chromium's runtime libraries under their Ubuntu 20.04 names. Playwright's
# --with-deps list for its ubuntu22.04 build names fonts-unifont, which 20.04
# does not have, so the release container installs this list instead.
install_release_container_browser_deps() {
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq --no-install-recommends \
    libasound2 libatk-bridge2.0-0 libatk1.0-0 libatspi2.0-0 libcairo2 libcups2 \
    libdbus-1-3 libdrm2 libgbm1 libglib2.0-0 libnspr4 libnss3 libpango-1.0-0 \
    libwayland-client0 libx11-6 libxcb1 libxcomposite1 libxdamage1 libxext6 \
    libxfixes3 libxkbcommon0 libxrandr2 libfontconfig1 libfreetype6 \
    fonts-liberation fonts-noto-color-emoji fonts-freefont-ttf xvfb >/dev/null
}

prepare_chromium() {
  local playwright_install_args=(install chromium)
  if [[ "${SDN_CI_BROWSER_DEPS:-}" == release-container ]]; then
    install_release_container_browser_deps
  elif [[ "${CI:-}" == "true" || "${CI:-}" == "1" ]]; then
    playwright_install_args=(install --with-deps chromium)
  fi
  (cd "$ROOT/sdn-js" && npm_config_cache="$ROOT/.npm-cache" npx --yes playwright "${playwright_install_args[@]}" >/dev/null)
}

run_sdn_js() {
  prepare_go_toolchain no-aot
  prepare_sdn_js_deps

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
  # Build exactly as npm test does, then share real handler responses with Vitest.
  # Focused npm test invocations do not require the Go schema exporter.
  (cd "$ROOT/sdn-js" && npm_config_cache="$ROOT/.npm-cache" npm run build:core \
    && SDN_REQUIRE_GO_INTEROP=1 node scripts/test-with-standards.mjs)
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
  prepare_chromium
  (cd "$ROOT/sdn-js" && SDN_REQUIRE_BROWSER_INTEROP=1 npm_config_cache="$ROOT/.npm-cache" npm run test:browser-interop)
  pass "sdn-js browser direct interop"
}

run_dashboard() {
  prepare_sdn_js_deps
  prepare_chromium
  prepare_go_toolchain no-aot

  local binary="$ROOT/.ci-artifacts/spacedatanetwork-dashboard"
  local results="$ROOT/.ci-artifacts/dashboard"
  mkdir -p "$results"

  step "Embedded dashboard node build"
  (cd "$ROOT/sdn-server" && "$ROOT/scripts/go-with-wasmedge.sh" build -o "$binary" ./cmd/spacedatanetwork)
  pass "embedded dashboard node build"

  step "Embedded dashboard E2E"
  rm -f "$results/dashboard-embedded.json"
  if ! node "$ROOT/sdn-js/scripts/dashboard-engine.e2e.mjs" "$binary" "$results"; then
    if [[ -f "$results/dashboard-embedded.json" ]]; then
      cat "$results/dashboard-embedded.json"
    fi
    fail "embedded dashboard E2E"
  fi
  pass "embedded dashboard E2E"
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
  # module-placement.test.mjs resolves space-data-module-sdk from sdn-js/ rather
  # than the root (createRequire over sdn-js/package.json), and this lane is the
  # one that has installed both trees. Hiding only the root node_modules is why
  # its CI failure did not reproduce locally.
  step "module placement"
  (cd "$ROOT" && npm_config_cache="$ROOT/.npm-cache" node --test scripts/module-placement.test.mjs)
  pass "module placement"
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
    run_migrations
    run_go_builds
    run_sdn_js
    run_module_delivery_compat
    run_plugin_demo
    ;;
  full|all)
    run_preflight
    run_kubo_pin
    run_go
    run_migrations
    run_go_heavy
    run_go_race
    run_go_builds
    run_dashboard
    run_sdn_js
    run_module_delivery_compat
    run_plugin_demo
    run_encryption
    ;;
  publication) run_publication ;;
  prefix-build) run_prefix_build ;;
  prefix-pack) run_prefix_pack ;;
  prefix-unpack) run_prefix_unpack ;;
  runtime) run_runtime ;;
  migrations) run_migrations ;;
  update-channel) run_update_channel ;;
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
  dashboard)
    run_dashboard
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
    echo -e "${RED}Usage: $0 [quick|full|preflight|go|heavy|builds|race|js|dashboard|kubo-pin|delivery|plugin|demo|encryption|publication|runtime|migrations|update-channel|prefix-build|prefix-pack|prefix-unpack]${NC}"
    exit 1
    ;;
esac

echo -e "\n${GREEN}CI PASSED (${MODE})${NC}"
