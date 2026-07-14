#!/usr/bin/env bash

set -Eeuo pipefail

# The SDN_KUBO_MIGRATION_* overrides are for the fixture test only. An operator
# should run this script with the production defaults below.
SOURCE_REPO="${SDN_KUBO_MIGRATION_SOURCE_REPO:-/var/lib/ipfs}"
DESTINATION_REPO="${SDN_KUBO_MIGRATION_DESTINATION_REPO:-/mnt/volume_nyc3_01/ipfs}"
VOLUME_MOUNT="${SDN_KUBO_MIGRATION_VOLUME_MOUNT:-/mnt/volume_nyc3_01}"
DROP_IN="${SDN_KUBO_MIGRATION_DROP_IN:-/etc/systemd/system/ipfs.service.d/20-volume-repo.conf}"
REPO_OWNER="${SDN_KUBO_MIGRATION_REPO_OWNER:-ipfs:ipfs}"
STORAGE_MAX="${SDN_KUBO_MIGRATION_STORAGE_MAX:-120GB}"
HEADROOM_KIB="${SDN_KUBO_MIGRATION_HEADROOM_KIB:-10485760}"
HTTP_ATTEMPTS="${SDN_KUBO_MIGRATION_HTTP_ATTEMPTS:-30}"
HTTP_DELAY_SECONDS="${SDN_KUBO_MIGRATION_HTTP_DELAY_SECONDS:-1}"

readonly DROP_IN REPO_OWNER STORAGE_MAX
readonly HEADROOM_KIB HTTP_ATTEMPTS HTTP_DELAY_SECONDS
readonly SDN_SERVICE="space-data-network.service"
readonly KUBO_SERVICE="ipfs.service"
readonly KUBO_API_URL="http://127.0.0.1:5002/api/v0/id"
readonly KUBO_GATEWAY_URL="http://127.0.0.1:8091"

STATE_DIR=""
DROP_IN_BACKUP=""
DROP_IN_TEMP=""
DROP_IN_DIR="$(dirname "$DROP_IN")"
SOURCE_PINS_FILE=""
SAMPLE_PINS_FILE=""
POST_PINS_FILE=""
mutation_started=0
drop_in_had_previous=0
drop_in_dir_existed=0
prior_sdn_active=0
prior_kubo_active=0
failure_line=0

log() {
    printf '[kubo-repo-migration] %s\n' "$*"
}

fail() {
    printf '[kubo-repo-migration] ERROR: %s\n' "$*" >&2
    return 1
}

restore_service_state() {
    local unit=$1
    local was_active=$2

    if ((was_active)); then
        systemctl start "$unit"
    else
        systemctl stop "$unit"
    fi
}

rollback() {
    local rollback_status=0

    set +e
    log "Migration failed; restoring the prior Kubo drop-in and service states."

    systemctl stop "$SDN_SERVICE" || rollback_status=1
    systemctl stop "$KUBO_SERVICE" || rollback_status=1

    if ((drop_in_had_previous)); then
        mkdir -p "$DROP_IN_DIR" || rollback_status=1
        rm -f "$DROP_IN" || rollback_status=1
        cp -a "$DROP_IN_BACKUP" "$DROP_IN" || rollback_status=1
    else
        rm -f "$DROP_IN" || rollback_status=1
        if ((!drop_in_dir_existed)); then
            rmdir "$DROP_IN_DIR" 2>/dev/null || true
        fi
    fi

    systemctl daemon-reload || rollback_status=1
    restore_service_state "$KUBO_SERVICE" "$prior_kubo_active" || rollback_status=1
    restore_service_state "$SDN_SERVICE" "$prior_sdn_active" || rollback_status=1

    if ((rollback_status)); then
        printf '[kubo-repo-migration] ERROR: rollback encountered an error; inspect both services before retrying.\n' >&2
    else
        log "Rollback complete. The source repository was not changed."
    fi
    return "$rollback_status"
}

finish() {
    local status=$?

    trap - ERR EXIT INT TERM
    if ((status != 0)); then
        printf '[kubo-repo-migration] ERROR: command failed near line %s.\n' "$failure_line" >&2
        if ((mutation_started)); then
            rollback || true
        fi
    fi

    if [[ -n "$DROP_IN_TEMP" ]]; then
        rm -f "$DROP_IN_TEMP" || true
    fi
    if [[ -n "$STATE_DIR" ]]; then
        rm -rf "$STATE_DIR" || true
    fi
    exit "$status"
}

trap 'failure_line=$LINENO' ERR
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

capture_recursive_pins() {
    local repo=$1
    IPFS_PATH="$repo" ipfs pin ls --type=recursive --quiet | LC_ALL=C sort -u
}

pin_count() {
    awk 'NF { count++ } END { print count + 0 }' "$1"
}

wait_for_api() {
    local attempt
    for ((attempt = 1; attempt <= HTTP_ATTEMPTS; attempt++)); do
        if curl --fail --silent --show-error --max-time 10 --request POST "$KUBO_API_URL" >/dev/null; then
            return 0
        fi
        if ((attempt < HTTP_ATTEMPTS)); then
            sleep "$HTTP_DELAY_SECONDS"
        fi
    done
    fail "Kubo API did not become ready at $KUBO_API_URL."
}

wait_for_gateway() {
    local cid=$1
    local attempt
    local url="${KUBO_GATEWAY_URL}/ipfs/${cid}"

    for ((attempt = 1; attempt <= HTTP_ATTEMPTS; attempt++)); do
        if curl --fail --silent --show-error --location --head --max-time 20 "$url" >/dev/null; then
            return 0
        fi
        if ((attempt < HTTP_ATTEMPTS)); then
            sleep "$HTTP_DELAY_SECONDS"
        fi
    done
    fail "Kubo gateway did not serve sample pin $cid at $url."
}

if ((EUID != 0)) && [[ "${SDN_KUBO_MIGRATION_ALLOW_NON_ROOT:-0}" != "1" ]]; then
    fail "Run this migration as root."
fi

for required_command in systemctl ipfs rsync findmnt realpath df du curl chown xargs; do
    command -v "$required_command" >/dev/null 2>&1 || fail "Required command is missing: $required_command"
done

[[ -d "$SOURCE_REPO" ]] || fail "Source Kubo repository does not exist: $SOURCE_REPO"
canonical_source_repo="$(realpath "$SOURCE_REPO")" || fail "Could not canonicalize source repository: $SOURCE_REPO"

kubo_load_state="$(systemctl show --property=LoadState --value "$KUBO_SERVICE")" || \
    fail "Could not inspect the load state of $KUBO_SERVICE."
[[ "$kubo_load_state" == "loaded" ]] || \
    fail "$KUBO_SERVICE is not loaded (LoadState=${kubo_load_state:-<empty>}); refusing migration."
kubo_environment="$(systemctl show --property=Environment --value "$KUBO_SERVICE")" || \
    fail "Could not inspect the effective Environment of $KUBO_SERVICE."
if [[ "$kubo_environment" == *$'\n'* || "$kubo_environment" == *$'\r'* ]]; then
    fail "$KUBO_SERVICE effective IPFS_PATH is ambiguous; refusing migration."
fi
if ! kubo_environment_assignments="$(
    printf '%s\n' "$kubo_environment" |
        xargs -n 1 printf '%s\n' 2>/dev/null
)"; then
    fail "$KUBO_SERVICE effective IPFS_PATH is ambiguous; refusing migration."
fi
effective_ipfs_path=""
ipfs_path_assignment_count=0
while IFS= read -r environment_assignment; do
    [[ -n "$environment_assignment" ]] || continue
    case "$environment_assignment" in
        IPFS_PATH=*)
            ((ipfs_path_assignment_count += 1))
            effective_ipfs_path="${environment_assignment#IPFS_PATH=}"
            ;;
    esac
done <<< "$kubo_environment_assignments"
if ((ipfs_path_assignment_count > 1)); then
    fail "$KUBO_SERVICE effective IPFS_PATH is ambiguous; refusing migration."
fi
if ((ipfs_path_assignment_count == 0)); then
    fail "$KUBO_SERVICE effective IPFS_PATH is missing; refusing migration."
fi
[[ "$effective_ipfs_path" == "$canonical_source_repo" ]] || \
    fail "$KUBO_SERVICE effective IPFS_PATH is mismatched; refusing migration."

[[ ! -L "$DESTINATION_REPO" ]] || fail "Destination repository must not be a symlink: $DESTINATION_REPO"
canonical_volume_mount="$(realpath "$VOLUME_MOUNT")" || fail "Could not canonicalize volume mount: $VOLUME_MOUNT"

if ! mounted_volume_target="$(findmnt --noheadings --mountpoint "$VOLUME_MOUNT" --output TARGET)"; then
    fail "$VOLUME_MOUNT is not a mounted filesystem; refusing to copy into the root filesystem."
fi
canonical_mounted_volume="$(realpath "$mounted_volume_target")" || \
    fail "Could not canonicalize mounted volume target: $mounted_volume_target"
[[ "$canonical_mounted_volume" == "$canonical_volume_mount" ]] || \
    fail "Mounted volume target does not match the canonical volume: $mounted_volume_target"

if [[ -e "$DESTINATION_REPO" ]]; then
    [[ -d "$DESTINATION_REPO" ]] || fail "Destination repository exists but is not a directory: $DESTINATION_REPO"
    canonical_destination_repo="$(realpath "$DESTINATION_REPO")" || \
        fail "Could not canonicalize destination repository: $DESTINATION_REPO"
    destination_mount_probe="$canonical_destination_repo"
else
    destination_parent="$(dirname "$DESTINATION_REPO")"
    destination_name="$(basename "$DESTINATION_REPO")"
    [[ "$destination_name" != "." && "$destination_name" != ".." ]] || \
        fail "Destination repository must name a child directory: $DESTINATION_REPO"
    [[ -d "$destination_parent" ]] || fail "Destination parent does not exist: $destination_parent"
    canonical_destination_parent="$(realpath "$destination_parent")" || \
        fail "Could not canonicalize destination parent: $destination_parent"
    canonical_destination_repo="${canonical_destination_parent%/}/${destination_name}"
    destination_mount_probe="$canonical_destination_parent"
fi

if [[ "$canonical_destination_repo" == "$canonical_source_repo" ]]; then
    fail "Canonical source and destination are the same path: $canonical_source_repo"
fi
case "$canonical_destination_repo" in
    "$canonical_source_repo"/*)
        fail "Canonical destination is nested inside the source repository: $canonical_destination_repo"
        ;;
esac
case "$canonical_destination_repo" in
    "$canonical_volume_mount"/*) ;;
    *) fail "Canonical destination is outside the exact volume: $canonical_destination_repo" ;;
esac

if ! destination_mount_target="$(findmnt --noheadings --target "$destination_mount_probe" --output TARGET)"; then
    fail "Could not resolve the destination filesystem for $destination_mount_probe."
fi
canonical_destination_mount="$(realpath "$destination_mount_target")" || \
    fail "Could not canonicalize destination mount target: $destination_mount_target"
if [[ "$canonical_destination_mount" != "$canonical_volume_mount" ]]; then
    fail "Destination resolves to a nested mount instead of the exact volume: $destination_mount_target"
fi

SOURCE_REPO="$canonical_source_repo"
VOLUME_MOUNT="$canonical_volume_mount"
DESTINATION_REPO="$canonical_destination_repo"
readonly SOURCE_REPO VOLUME_MOUNT DESTINATION_REPO

if [[ -e "$DESTINATION_REPO" || -L "$DESTINATION_REPO" ]]; then
    [[ -d "$DESTINATION_REPO" ]] || fail "Destination repository exists but is not a directory: $DESTINATION_REPO"
    shopt -s nullglob dotglob
    destination_entries=("$DESTINATION_REPO"/*)
    shopt -u nullglob dotglob
    ((${#destination_entries[@]} == 0)) || fail "Destination repository is not empty: $DESTINATION_REPO"
fi

[[ "$HEADROOM_KIB" =~ ^[0-9]+$ ]] || fail "HEADROOM_KIB must be a non-negative integer."
[[ "$HTTP_ATTEMPTS" =~ ^[1-9][0-9]*$ ]] || fail "HTTP_ATTEMPTS must be a positive integer."
[[ "$HTTP_DELAY_SECONDS" =~ ^[0-9]+$ ]] || fail "HTTP_DELAY_SECONDS must be a non-negative integer."

source_kib="$(du -sk "$SOURCE_REPO" | awk 'NR == 1 { print $1 }')"
available_kib="$(df -Pk "$VOLUME_MOUNT" | awk 'NR == 2 { print $4 }')"
[[ "$source_kib" =~ ^[0-9]+$ ]] || fail "Could not determine source repository size."
[[ "$available_kib" =~ ^[0-9]+$ ]] || fail "Could not determine free space on $VOLUME_MOUNT."
required_kib=$((source_kib + HEADROOM_KIB))
if ((available_kib < required_kib)); then
    fail "Insufficient free space on $VOLUME_MOUNT: ${available_kib} KiB available, ${required_kib} KiB required."
fi

STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sdn-kubo-migration.XXXXXX")"
DROP_IN_BACKUP="${STATE_DIR}/previous-drop-in.conf"
SOURCE_PINS_FILE="${STATE_DIR}/source-recursive-pins.txt"
SAMPLE_PINS_FILE="${STATE_DIR}/sample-recursive-pins.txt"
POST_PINS_FILE="${STATE_DIR}/post-recursive-pins.txt"

source_peer_id="$(IPFS_PATH="$SOURCE_REPO" ipfs id --format='<id>')"
[[ -n "$source_peer_id" ]] || fail "Could not capture the source Kubo peer ID."
capture_recursive_pins "$SOURCE_REPO" > "$SOURCE_PINS_FILE"
source_pin_count="$(pin_count "$SOURCE_PINS_FILE")"
awk 'NF { print; count++; if (count == 5) exit }' "$SOURCE_PINS_FILE" > "$SAMPLE_PINS_FILE"
sample_pin_count="$(pin_count "$SAMPLE_PINS_FILE")"
((sample_pin_count == 5)) || fail "At least five recursive pins are required for deterministic verification."

if systemctl is-active --quiet "$SDN_SERVICE"; then
    prior_sdn_active=1
fi
if systemctl is-active --quiet "$KUBO_SERVICE"; then
    prior_kubo_active=1
fi

if [[ -d "$DROP_IN_DIR" ]]; then
    drop_in_dir_existed=1
fi
if [[ -e "$DROP_IN" || -L "$DROP_IN" ]]; then
    cp -a "$DROP_IN" "$DROP_IN_BACKUP"
    drop_in_had_previous=1
fi

log "Preflight passed: peer $source_peer_id, $source_pin_count recursive pins, $source_kib KiB source repository."
mutation_started=1

# Stop the SDN consumer first, then stop the Kubo writer before copying.
systemctl stop "$SDN_SERVICE"
systemctl stop "$KUBO_SERVICE"

mkdir -p "$DESTINATION_REPO"
rsync -aHAX --numeric-ids "${SOURCE_REPO%/}/" "${DESTINATION_REPO%/}/"
IPFS_PATH="$DESTINATION_REPO" ipfs config Datastore.StorageMax "$STORAGE_MAX"
chown -R "$REPO_OWNER" "$DESTINATION_REPO"

mkdir -p "$DROP_IN_DIR"
DROP_IN_TEMP="${DROP_IN}.tmp.$$"
printf '[Service]\nEnvironment=IPFS_PATH=%s\nReadWritePaths=%s\n' \
    "$DESTINATION_REPO" "$DESTINATION_REPO" > "$DROP_IN_TEMP"
chmod 0644 "$DROP_IN_TEMP"
mv -f "$DROP_IN_TEMP" "$DROP_IN"
DROP_IN_TEMP=""
systemctl daemon-reload

systemctl start "$KUBO_SERVICE"
systemctl is-active --quiet "$KUBO_SERVICE" || fail "Kubo did not remain active after migration."
wait_for_api
destination_peer_id="$(IPFS_PATH="$DESTINATION_REPO" ipfs id --format='<id>')"
if [[ "$destination_peer_id" != "$source_peer_id" ]]; then
    fail "Peer ID mismatch after migration: expected $source_peer_id, got $destination_peer_id."
fi

capture_recursive_pins "$DESTINATION_REPO" > "$POST_PINS_FILE"
post_pin_count="$(pin_count "$POST_PINS_FILE")"
if ((post_pin_count < source_pin_count)); then
    fail "Recursive pin count decreased after migration: before=$source_pin_count after=$post_pin_count."
fi

while IFS= read -r sample_cid; do
    if ! IPFS_PATH="$DESTINATION_REPO" ipfs pin ls --type=recursive --quiet "$sample_cid" >/dev/null; then
        fail "Sample recursive pin missing after migration: $sample_cid."
    fi
done < "$SAMPLE_PINS_FILE"

first_sample_cid="$(head -n 1 "$SAMPLE_PINS_FILE")"
wait_for_gateway "$first_sample_cid"

systemctl start "$SDN_SERVICE"
systemctl is-active --quiet "$SDN_SERVICE" || fail "SDN did not remain active after migration."

mutation_started=0
log "Migration verified: peer $destination_peer_id, $post_pin_count recursive pins, API and gateway healthy."
log "Source repository retained at $SOURCE_REPO."
