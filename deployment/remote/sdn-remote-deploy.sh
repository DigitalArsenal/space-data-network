#!/usr/bin/env bash
#
# sdn-remote-deploy.sh — one command, fresh remote server -> running SDN node.
#
#   ./deployment/remote/sdn-remote-deploy.sh deploy \
#       --host root@203.0.113.10 --domain node.example.org
#
# WHAT THIS DOES AND WHY IT DOES IT THAT WAY
# ------------------------------------------
# 1. Builds the release image LOCALLY for linux/amd64 and streams it to the
#    host with `docker save | ssh | docker load`. It never builds on the host
#    (standing owner law) and never needs a registry, so no registry
#    credentials ever land on the server and no Go source is shipped there.
#    `--registry` switches to a registry push for operators who already run one.
#
# 2. Feeds private key material without it ever touching an image, a build arg,
#    the process environment, `docker inspect`, argv, or your shell history:
#      - the MNEMONIC is read with echo off and piped straight down the ssh
#        channel into a tmpfs file (/run/sdn-secrets), imported+encrypted by the
#        node on first boot, then shredded;
#      - the AT-REST KEY PASSWORD is generated ON THE HOST (it never crosses the
#        wire at all) into a 0400 file, and reaches the node as a mounted file;
#      - the OPERATOR USERNAME/PASSWORD are never sent at all. You claim the
#        node with the one-time setup token over TLS.
#
# 3. Pins the container hostname, because the credential keystore root key is
#    hostname-bound and `docker compose up -d` re-rolls an unpinned hostname on
#    every upgrade (see docker-compose.yaml.template).
#
# 4. Takes a stopped-state volume backup before every cutover, and keeps the
#    previous image tag so `rollback` is a real, tested path.
#
# Requires locally: docker (with buildx), ssh. Requires on the host: docker with
# the compose plugin. Nothing else.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# ---- ship-time constants -----------------------------------------------------
REMOTE_DIR="/opt/sdn"                       # compose + rendered config
REMOTE_KEYDIR="/etc/sdn"                    # durable at-rest key password
REMOTE_SECRETDIR="/run/sdn-secrets"         # tmpfs, first-boot mnemonic only
REMOTE_BACKUPDIR="/var/backups/sdn"
CONTAINER_UID=1000                          # the `sdn` user in the image
PLATFORM_DEFAULT="linux/amd64"

# ---- options -----------------------------------------------------------------
CMD=""
HOST=""
SSH_PORT="22"
SSH_KEY=""
DOMAIN=""
NODE_NAME=""
IMAGE_TAG=""
PLATFORM="${PLATFORM_DEFAULT}"
REGISTRY=""
MNEMONIC_MODE="prompt"                      # prompt | generate | none
ASSUME_YES="no"
DRY_RUN="no"
NO_BUILD="no"

RED=$'\033[0;31m'; GRN=$'\033[0;32m'; YLW=$'\033[1;33m'; BLU=$'\033[0;34m'; NC=$'\033[0m'
info()  { printf '%s[INFO]%s %s\n'  "$BLU" "$NC" "$*"; }
ok()    { printf '%s[ OK ]%s %s\n'  "$GRN" "$NC" "$*"; }
warn()  { printf '%s[WARN]%s %s\n'  "$YLW" "$NC" "$*" >&2; }
die()   { printf '%s[FAIL]%s %s\n'  "$RED" "$NC" "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
Usage: sdn-remote-deploy.sh COMMAND [options]

Commands:
  deploy         Build locally, ship the image, render config, start the node
  status         Show container state + node status from the host
  logs           Tail the node log (secrets never appear here by design)
  backup         Stopped-state snapshot of the data volume
  rollback       Restore the previous image + the newest stopped-state backup
  shred-secrets  Wipe the tmpfs mnemonic staging file on the host

Required for every remote command:
  --host USER@HOST       ssh destination (e.g. root@203.0.113.10)

Options:
  --port N               ssh port (default 22)
  --ssh-key FILE         ssh identity file
  --domain FQDN          enable managed TLS (Let's Encrypt) for this hostname.
                         Without it the admin API is bound to loopback only and
                         you must reach it through an ssh tunnel.
  --name NAME            stable node/container hostname (default derived from
                         --domain, else sdn-node). NEVER change it afterwards.
  --tag TAG              image tag to build/ship (default sdn-node:<git-sha>)
  --platform P           image platform (default linux/amd64)
  --registry REF         push/pull via this registry instead of save|ssh|load
  --no-build             ship an image already built and verified locally
                         (requires --tag); still never builds on the host
  --generate-mnemonic    let the node generate a fresh 24-word mnemonic
                         (default: prompt for yours, echo off)
  --no-mnemonic          ship nothing; reuse the identity already on the volume
  -y, --yes              do not ask for confirmation before cutover
  -n, --dry-run          print what would happen; touch nothing
  -h, --help             this text
EOF
}

# ---- arg parsing -------------------------------------------------------------
[[ $# -gt 0 ]] || { usage; exit 1; }
CMD="$1"; shift
while [[ $# -gt 0 ]]; do
  case "$1" in
    --host)     HOST="$2"; shift 2 ;;
    --port)     SSH_PORT="$2"; shift 2 ;;
    --ssh-key)  SSH_KEY="$2"; shift 2 ;;
    --domain)   DOMAIN="$2"; shift 2 ;;
    --name)     NODE_NAME="$2"; shift 2 ;;
    --tag)      IMAGE_TAG="$2"; shift 2 ;;
    --platform) PLATFORM="$2"; shift 2 ;;
    --registry) REGISTRY="$2"; shift 2 ;;
    --no-build) NO_BUILD="yes"; shift ;;
    --generate-mnemonic) MNEMONIC_MODE="generate"; shift ;;
    --no-mnemonic)       MNEMONIC_MODE="none"; shift ;;
    -y|--yes)   ASSUME_YES="yes"; shift ;;
    -n|--dry-run) DRY_RUN="yes"; shift ;;
    -h|--help)  usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

# ---- ssh plumbing ------------------------------------------------------------
# SSH_OPTS is built once. Kept as an array (not a string) so nothing is
# re-split; macOS ships bash 3.2, so no mapfile/readarray here.
SSH_OPTS=()
init_ssh_opts() {
  SSH_OPTS=(-p "$SSH_PORT" -o BatchMode=yes -o StrictHostKeyChecking=accept-new)
  [[ -n "$SSH_KEY" ]] && SSH_OPTS+=(-i "$SSH_KEY")
}

# rsh runs a command on the host. Secrets are NEVER passed here as arguments —
# they are only ever piped on stdin (see feed_mnemonic).
rsh() {
  if [[ "$DRY_RUN" == "yes" ]]; then
    printf '%s[dry-run]%s ssh %s -- %s\n' "$YLW" "$NC" "$HOST" "$*"
    return 0
  fi
  ssh "${SSH_OPTS[@]}" "$HOST" "$@"
}

require_host() { [[ -n "$HOST" ]] || die "--host USER@HOST is required"; init_ssh_opts; }

preflight() {
  require_host
  info "Preflight against ${HOST}…"
  command -v docker >/dev/null || die "docker not found locally"
  docker buildx version >/dev/null 2>&1 || die "docker buildx not available locally"
  [[ "$DRY_RUN" == "yes" ]] && return 0
  rsh 'command -v docker >/dev/null' \
    || die "docker is not installed on the host. Install it first: curl -fsSL https://get.docker.com | sh"
  rsh 'docker compose version >/dev/null' \
    || die "the docker compose plugin is missing on the host"
  ok "host has docker + compose"
}

# ---- image ------------------------------------------------------------------
default_tag() {
  local sha; sha="$(git -C "$PROJECT_ROOT" rev-parse --short HEAD 2>/dev/null || echo dev)"
  printf 'sdn-node:%s' "$sha"
}

build_image() {
  [[ -n "$IMAGE_TAG" ]] || IMAGE_TAG="$(default_tag)"
  if [[ "$NO_BUILD" == "yes" ]]; then
    docker image inspect "$IMAGE_TAG" >/dev/null 2>&1 \
      || die "--no-build was given but ${IMAGE_TAG} is not present locally"
    ok "reusing locally built ${IMAGE_TAG} ($(docker image inspect "$IMAGE_TAG" --format '{{.Architecture}}'))"
    return 0
  fi
  info "Building ${IMAGE_TAG} for ${PLATFORM} LOCALLY (never on the host)…"
  if [[ "$DRY_RUN" == "yes" ]]; then
    printf '%s[dry-run]%s docker buildx build --platform %s -t %s .\n' "$YLW" "$NC" "$PLATFORM" "$IMAGE_TAG"
    return 0
  fi
  # The IPFS webui is a build-context prerequisite (Dockerfile COPY webui/build/).
  [[ -f "${PROJECT_ROOT}/webui/build/index.html" ]] \
    || die "webui/build is missing — build the IPFS WebUI before deploying"

  # Stage the same-origin wallet sign-in assets into the build context. Skipping
  # this yields an image whose dashboard reports "sign-in unavailable", i.e. a
  # node nobody can administer, so it is not optional on the deploy path.
  info "Staging same-origin wallet sign-in assets…"
  "${PROJECT_ROOT}/deployment/wallet-wasm/stage-wallet-wasm.sh" \
    "${PROJECT_ROOT}/deployment/wallet-wasm/staged/wallet-wasm" \
    "${PROJECT_ROOT}/deployment/wallet-wasm/staged/wallet-ui" >/dev/null \
    || die "wallet asset staging failed — run deployment/wallet-wasm/stage-wallet-wasm.sh by hand to see why"
  docker buildx build \
    --platform "$PLATFORM" \
    -f "${PROJECT_ROOT}/deployment/docker/Dockerfile" \
    -t "$IMAGE_TAG" --load "$PROJECT_ROOT"
  ok "built ${IMAGE_TAG} ($(docker image inspect "$IMAGE_TAG" --format '{{.Architecture}}'))"
}

ship_image() {
  if [[ -n "$REGISTRY" ]]; then
    info "Pushing ${IMAGE_TAG} to ${REGISTRY} and pulling it on the host…"
    if [[ "$DRY_RUN" == "yes" ]]; then return 0; fi
    docker tag "$IMAGE_TAG" "${REGISTRY}/${IMAGE_TAG}"
    docker push "${REGISTRY}/${IMAGE_TAG}"
    rsh "docker pull '${REGISTRY}/${IMAGE_TAG}' && docker tag '${REGISTRY}/${IMAGE_TAG}' '${IMAGE_TAG}'"
    ok "image on host via registry"
    return 0
  fi

  local size; size="$(docker image inspect "$IMAGE_TAG" --format '{{.Size}}' 2>/dev/null || echo 0)"
  info "Streaming ${IMAGE_TAG} ($(( size / 1024 / 1024 )) MB uncompressed) over ssh — no registry, no credentials on the host…"
  if [[ "$DRY_RUN" == "yes" ]]; then
    printf '%s[dry-run]%s docker save %s | gzip | ssh %s docker load\n' "$YLW" "$NC" "$IMAGE_TAG" "$HOST"
    return 0
  fi
  docker save "$IMAGE_TAG" | gzip -1 | ssh "${SSH_OPTS[@]}" "$HOST" 'gunzip | docker load'
  rsh "docker image inspect '${IMAGE_TAG}' >/dev/null" || die "image did not land on the host"
  ok "image loaded on host"
}

# ---- rendered artifacts ------------------------------------------------------
# render_template FILE [KEY=VALUE...] [-- BLOCK_KEY BLOCK_TEXT]
# Pure bash so it behaves identically under macOS bash 3.2 / BSD awk, which
# cannot take multi-line values in `awk -v`. Scalar keys are substituted inline;
# the single block key replaces its whole line with a multi-line body.
render_template() {
  local tmpl="$1"; shift
  local -a pairs=()
  while [[ $# -gt 0 && "$1" != "--" ]]; do pairs+=("$1"); shift; done
  local block_key="" block_body=""
  if [[ "${1:-}" == "--" ]]; then shift; block_key="${1:-}"; block_body="${2:-}"; fi

  local line pair k v
  while IFS= read -r line || [[ -n "$line" ]]; do
    # Exact (whitespace-trimmed) match only. A substring match would also fire
    # on the template's own header comment describing the placeholder.
    if [[ -n "$block_key" && "$(printf '%s' "$line" | tr -d '[:space:]')" == "$block_key" ]]; then
      printf '%s\n' "$block_body"
      continue
    fi
    for pair in "${pairs[@]}"; do
      k="${pair%%=*}"; v="${pair#*=}"
      line="${line//$k/$v}"
    done
    printf '%s\n' "$line"
  done < "$tmpl"
}

render_node_yaml() {
  local tls_block admin_listen
  if [[ -n "$DOMAIN" ]]; then
    # Managed TLS conditions (Hermes/tlsmgr manager.go:193-199): tls_hosts MUST
    # be non-empty or autocert is never constructed and the listener serves no
    # certificate; the ACME cache MUST live on the persistent volume or every
    # container recreate burns another of Let's Encrypt's 5 certs/domain/week.
    admin_listen="0.0.0.0:443"
    tls_block=$(cat <<EOF
  tls_mode: managed
  tls_hosts:
    - ${DOMAIN}
  tls_cache_dir: /app/data/tls
  http_challenge_addr: 0.0.0.0:80
EOF
)
  else
    admin_listen="0.0.0.0:5001"
    tls_block="  tls_mode: disabled"
  fi
  render_template "${SCRIPT_DIR}/node.yaml.template" \
    "__ADMIN_LISTEN__=${admin_listen}" -- "__TLS_BLOCK__" "$tls_block"
}

render_compose() {
  local ports
  if [[ -n "$DOMAIN" ]]; then
    ports=$(cat <<'EOF'
      - "80:80"
      - "443:443"
      - "4001:4001"
      - "4001:4001/udp"
      - "8080:8080"
EOF
)
  else
    # No domain => the admin surface is unauthenticated-by-TLS, so it is bound
    # to loopback on the host and reachable only through an ssh tunnel.
    ports=$(cat <<'EOF'
      - "127.0.0.1:5001:5001"
      - "4001:4001"
      - "4001:4001/udp"
      - "8080:8080"
EOF
)
  fi
  render_template "${SCRIPT_DIR}/docker-compose.yaml.template" \
    "__IMAGE__=${IMAGE_TAG}" "__HOSTNAME__=${NODE_NAME}" "__RESTART__=unless-stopped" \
    -- "__PORTS__" "$ports"
}

# The env file carries NO secrets — only the *paths* the node reads them from.
render_env() {
  cat <<EOF
SDN_KEY_PASSWORD_FILE=/run/sdn-key/key_password
SDN_MNEMONIC_FILE=/run/secrets/sdn_mnemonic
EOF
}

# ---- secret feeding ----------------------------------------------------------
#
# ensure_key_password generates the at-rest password ON THE HOST. The value is
# never printed, never assigned to a local variable, and never crosses the ssh
# wire in either direction. If the file already exists it is left alone —
# regenerating it would make the existing encrypted mnemonic undecryptable.
ensure_key_password() {
  info "Ensuring the at-rest key password exists on the host…"
  rsh "set -e
    umask 077
    mkdir -p '${REMOTE_KEYDIR}'
    chmod 700 '${REMOTE_KEYDIR}'
    if [ -s '${REMOTE_KEYDIR}/key_password' ]; then
      echo 'existing key password kept'
    else
      head -c 32 /dev/urandom | base64 | tr -d '\n' > '${REMOTE_KEYDIR}/key_password'
      echo 'generated new key password'
    fi
    chown ${CONTAINER_UID}:${CONTAINER_UID} '${REMOTE_KEYDIR}/key_password'
    chmod 400 '${REMOTE_KEYDIR}/key_password'"
  ok "key password in place at ${REMOTE_KEYDIR}/key_password (0400, uid ${CONTAINER_UID})"
  warn "BACK THIS FILE UP. Without it the encrypted mnemonic on the data volume cannot be decrypted."
}

# image_supports_import reports whether the built image's binary actually
# honours SDN_MNEMONIC_FILE. Older binaries silently ignore it and generate
# their own identity instead — which would leave the operator's phrase sitting
# in the staging area while the node ran under a different key. We refuse to
# stage anything in that case rather than fail open.
image_supports_import() {
  docker run --rm --entrypoint sh "$IMAGE_TAG" -c \
    'grep -q SDN_MNEMONIC_FILE /app/spacedatanetwork' >/dev/null 2>&1
}

# feed_mnemonic reads the operator's mnemonic with echo off and pipes it
# straight into the ssh channel. It is never an argument, never an environment
# variable, never written to local disk, and never enters shell history.
feed_mnemonic() {
  case "$MNEMONIC_MODE" in
    none)
      info "Skipping mnemonic staging (--no-mnemonic): the node reuses the identity on its volume."
      rsh "mkdir -p '${REMOTE_SECRETDIR}' && chmod 700 '${REMOTE_SECRETDIR}'"
      return 0 ;;
    generate)
      info "The node will generate a fresh 24-word mnemonic on first boot."
      warn "Export and record it from the admin UI immediately after setup — it is your only identity backup."
      rsh "mkdir -p '${REMOTE_SECRETDIR}' && chmod 700 '${REMOTE_SECRETDIR}'"
      return 0 ;;
  esac

  if ! image_supports_import; then
    die "image ${IMAGE_TAG} does not support SDN_MNEMONIC_FILE, so it cannot import a phrase.
       Nothing was prompted for and nothing was sent. Either rebuild from a revision that
       carries the mnemonic-import contract, or deploy with --generate-mnemonic and export
       the generated phrase from the admin UI afterwards."
  fi

  printf '\n%sPaste your BIP-39 recovery phrase (input is hidden), or press Enter to have the node generate one:%s\n> ' "$BLU" "$NC"
  local phrase=""
  read -rs phrase || true
  printf '\n'
  if [[ -z "${phrase// /}" ]]; then
    info "No phrase entered — the node will generate a fresh 24-word mnemonic."
    rsh "mkdir -p '${REMOTE_SECRETDIR}' && chmod 700 '${REMOTE_SECRETDIR}'"
    return 0
  fi

  local words; words="$(printf '%s' "$phrase" | wc -w | tr -d ' ')"
  case "$words" in
    12|15|18|21|24) ;;
    *) phrase=""; die "a BIP-39 phrase has 12/15/18/21/24 words; got ${words}. Nothing was sent." ;;
  esac
  info "Staging a ${words}-word phrase into tmpfs on the host (never to disk, never to the image)…"

  if [[ "$DRY_RUN" == "yes" ]]; then phrase=""; warn "dry-run: phrase discarded, nothing sent"; return 0; fi

  # /run is a tmpfs on any systemd host, so this plaintext never reaches a disk.
  printf '%s' "$phrase" | ssh "${SSH_OPTS[@]}" "$HOST" "set -e
    umask 077
    mkdir -p '${REMOTE_SECRETDIR}'
    chmod 700 '${REMOTE_SECRETDIR}'
    cat > '${REMOTE_SECRETDIR}/sdn_mnemonic'
    chown ${CONTAINER_UID}:${CONTAINER_UID} '${REMOTE_SECRETDIR}/sdn_mnemonic'
    chmod 400 '${REMOTE_SECRETDIR}/sdn_mnemonic'
    mountpoint -q /run && echo 'staged on tmpfs' || echo 'WARNING: /run is not a tmpfs on this host'"
  phrase=""   # scrub the shell copy
  ok "phrase staged; it will be encrypted onto the volume and shredded after first boot"
}

shred_secrets() {
  require_host
  info "Shredding the mnemonic staging file on the host…"
  rsh "if [ -e '${REMOTE_SECRETDIR}/sdn_mnemonic' ]; then
         (command -v shred >/dev/null && shred -u '${REMOTE_SECRETDIR}/sdn_mnemonic') || rm -f '${REMOTE_SECRETDIR}/sdn_mnemonic'
         echo shredded
       else
         echo 'nothing staged'
       fi"
  ok "staging area clean"
}

# ---- backup / cutover / rollback --------------------------------------------
backup_volume() {
  require_host
  info "Taking a STOPPED-STATE backup of the data volume…"
  rsh "set -e
    mkdir -p '${REMOTE_BACKUPDIR}'
    if docker volume inspect sdn-data >/dev/null 2>&1; then
      cd '${REMOTE_DIR}' 2>/dev/null && docker compose stop || true
      ts=\$(date -u +%Y%m%dT%H%M%SZ)
      docker run --rm -v sdn-data:/data:ro -v '${REMOTE_BACKUPDIR}':/backup \
        busybox tar czf \"/backup/sdn-data-\${ts}.tgz\" -C /data .
      echo \"backup: ${REMOTE_BACKUPDIR}/sdn-data-\${ts}.tgz\"
    else
      echo 'no existing volume — first deploy, nothing to back up'
    fi"
  ok "backup step complete"
}

deploy() {
  require_host
  [[ -n "$NODE_NAME" ]] || NODE_NAME="${DOMAIN%%.*}"
  [[ -n "$NODE_NAME" ]] || NODE_NAME="sdn-node"

  preflight
  build_image
  [[ -n "$IMAGE_TAG" ]] || IMAGE_TAG="$(default_tag)"

  # Fail BEFORE anything is shipped or written, not halfway through a deploy.
  if [[ "$MNEMONIC_MODE" == "prompt" && "$DRY_RUN" != "yes" ]] && ! image_supports_import; then
    die "image ${IMAGE_TAG} predates the mnemonic-import contract (no SDN_MNEMONIC_FILE
       in the binary), so it cannot accept your recovery phrase. Nothing has been shipped
       and nothing was prompted for. Rebuild from a revision that carries the import
       contract, or re-run with --generate-mnemonic and export the generated phrase from
       the admin UI afterwards."
  fi

  if [[ -z "$DOMAIN" ]]; then
    warn "No --domain: managed TLS is OFF and the admin API will be bound to the host's loopback only."
    warn "Reach it with: ssh -L 5001:127.0.0.1:5001 ${HOST}"
  fi

  if [[ "$ASSUME_YES" != "yes" && "$DRY_RUN" != "yes" ]]; then
    printf '\nDeploy %s to %s as hostname %s%s? [y/N] ' "$IMAGE_TAG" "$HOST" "$NODE_NAME" \
      "$([[ -n "$DOMAIN" ]] && printf ' with TLS for %s' "$DOMAIN")"
    local reply; read -r reply
    [[ "$reply" =~ ^[Yy] ]] || die "aborted"
  fi

  backup_volume
  ship_image

  info "Rendering host configuration into ${REMOTE_DIR}…"
  rsh "mkdir -p '${REMOTE_DIR}'"
  if [[ "$DRY_RUN" != "yes" ]]; then
      render_node_yaml | ssh "${SSH_OPTS[@]}" "$HOST" "cat > '${REMOTE_DIR}/node.yaml'"
    render_compose   | ssh "${SSH_OPTS[@]}" "$HOST" "cat > '${REMOTE_DIR}/docker-compose.yaml'"
    render_env       | ssh "${SSH_OPTS[@]}" "$HOST" "umask 077; cat > '${REMOTE_DIR}/sdn.env'"
    # Record the previous image so `rollback` has somewhere to go.
    rsh "cd '${REMOTE_DIR}' && (grep -h '^SDN_CURRENT_IMAGE=' .deploy-state 2>/dev/null | sed 's/^SDN_CURRENT_IMAGE=/SDN_PREVIOUS_IMAGE=/' > .deploy-state.new || true)
         echo 'SDN_CURRENT_IMAGE=${IMAGE_TAG}' >> .deploy-state.new
         mv .deploy-state.new .deploy-state"
  fi
  ok "config rendered (no secrets are in any of these files)"

  ensure_key_password
  feed_mnemonic

  info "Starting the node…"
  rsh "cd '${REMOTE_DIR}' && docker compose up -d"
  [[ "$DRY_RUN" == "yes" ]] && { ok "dry-run complete"; return 0; }

  info "Waiting for the node to report healthy (first boot derives keys; allow a few minutes on small VMs)…"
  local waited=0
  while (( waited < 420 )); do
    if rsh "docker inspect --format '{{.State.Health.Status}}' sdn-node 2>/dev/null | grep -q healthy"; then
      ok "node is healthy"
      break
    fi
    sleep 10; waited=$(( waited + 10 ))
    (( waited % 60 == 0 )) && info "…still starting (${waited}s)"
  done
  (( waited >= 420 )) && warn "node did not report healthy within 7 minutes — check: $0 logs --host ${HOST}"

  # Prove the identity actually persisted INSIDE the volume before shredding.
  info "Verifying the encrypted mnemonic landed on the persistent volume…"
  if rsh "docker compose -f '${REMOTE_DIR}/docker-compose.yaml' exec -T sdn-node sh -c 'test -s /app/data/keys/mnemonic' 2>/dev/null"; then
    ok "/app/data/keys/mnemonic present inside the sdn-data volume"
    shred_secrets
  else
    warn "could not confirm /app/data/keys/mnemonic yet — NOT shredding the staged phrase."
    warn "Re-check, then run: $0 shred-secrets --host ${HOST}"
  fi

  printf '\n%s============== NEXT STEP: SIGN IN TO YOUR NODE ==============%s\n' "$GRN" "$NC"
  printf 'There is no username and no password to set. This node accepts its OWN\n'
  printf 'root identity as the administrator: sign-in is a challenge signed by the\n'
  printf 'seed behind the mnemonic on the data volume (main.go:1419-1432). The\n'
  printf 'recovery phrase you fed at deploy time IS your admin credential.\n\n'
  if [[ -n "$DOMAIN" ]]; then
    printf 'Open   https://%s/\n' "$DOMAIN"
  else
    printf 'Run    ssh -L 5001:127.0.0.1:5001 %s\n' "$HOST"
    printf 'Open   http://127.0.0.1:5001/\n'
  fi
  printf 'and use the in-page wallet sign-in with that phrase. The wallet is served\n'
  printf 'from this node itself (/wallet-wasm, /wallet-ui) — never from a website.\n'
  printf 'Grant any additional operators afterwards by xpub.\n'
  printf '%s============================================================%s\n' "$GRN" "$NC"
}

status() {
  require_host
  rsh "docker ps --filter name=sdn-node --format 'table {{.Names}}\t{{.Status}}\t{{.Image}}'
       echo
       docker compose -f '${REMOTE_DIR}/docker-compose.yaml' exec -T sdn-node sh -c \
         'wget -qO- --no-check-certificate https://localhost/api/v1/status 2>/dev/null || wget -qO- http://localhost:5001/api/v1/status 2>/dev/null' \
         | head -c 2000
       echo"
}

logs() { require_host; rsh "docker logs --tail 200 -f sdn-node"; }

rollback() {
  require_host
  warn "Rolling back to the previous image and the newest stopped-state backup."
  rsh "set -e
    cd '${REMOTE_DIR}'
    . ./.deploy-state
    [ -n \"\${SDN_PREVIOUS_IMAGE:-}\" ] || { echo 'no previous image recorded'; exit 1; }
    docker compose stop
    newest=\$(ls -1t '${REMOTE_BACKUPDIR}'/sdn-data-*.tgz 2>/dev/null | head -1)
    if [ -n \"\$newest\" ]; then
      docker run --rm -v sdn-data:/data -v '${REMOTE_BACKUPDIR}':/backup busybox \
        sh -c \"rm -rf /data/* /data/.[!.]* 2>/dev/null; tar xzf /backup/\$(basename \$newest) -C /data\"
      echo \"restored \$newest\"
    fi
    sed -i \"s|^    image: .*|    image: \${SDN_PREVIOUS_IMAGE}|\" docker-compose.yaml
    docker compose up -d
    echo \"rolled back to \${SDN_PREVIOUS_IMAGE}\""
  ok "rollback complete"
}

case "$CMD" in
  deploy)        deploy ;;
  status)        status ;;
  logs)          logs ;;
  backup)        backup_volume ;;
  rollback)      rollback ;;
  shred-secrets) shred_secrets ;;
  *) usage; die "unknown command: ${CMD}" ;;
esac
