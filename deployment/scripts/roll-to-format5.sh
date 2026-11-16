#!/usr/bin/env bash
# Offline bundle replacement with a retained format-4 recovery path.
set -euo pipefail
umask 077

usage() {
    echo 'Usage: roll-to-format5.sh UNIT BUNDLE_BASE STAGED CONFIG STORE READY_URL PEER LEDGER HOLDER TOKEN CONVERT_MINUTES READY_SECONDS [HOME]' >&2
    exit 2
}
[[ $# == 12 || $# == 13 ]] || usage
unit=$1 base=$2 staged=$3 config=$4 root=$5 ready_url=$6 peer=$7
ledger=$8 holder=$9 token=${10} minutes=${11} ready_seconds=${12}
node_home=${13:-/root}
[[ $unit != -* && $unit != */* && $unit == *.service ]] || usage
[[ $holder =~ ^[a-zA-Z0-9_.-]+$ && $token =~ ^[a-zA-Z0-9_.-]+$ ]] || usage
[[ $peer =~ ^[a-zA-Z0-9]+$ ]] || usage
[[ $minutes =~ ^[1-9][0-9]{0,4}$ && $ready_seconds =~ ^[1-9][0-9]{0,5}$ ]] || usage
[[ $ready_url == http://* || $ready_url == https://* ]] || usage
[[ $EUID == 0 ]] || { echo 'Root is required.' >&2; exit 2; }
# Canonical paths make rename and containment checks independent of spelling.
for path in "$base" "$staged" "$config" "$root" "$ledger" "$node_home"; do
    [[ $path == /* && $path != *$'\n'* ]] || usage
done
base=$(realpath -e -- "$base")
staged=$(realpath -m -- "$staged")
root=$(realpath -e -- "$root")
config=$(realpath -e -- "$config")
live=$base/sdn-bundle
stamp=$(date -u +%Y%m%dT%H%M%S)-$$
old=$base/sdn-bundle.old-$stamp
failed=$base/sdn-bundle.failed-$stamp
backup=${root}.roll-backup-$stamp
stage=PRECHECK stopped=0 conversion_pid= done_ok=0
side_files=(dataset-catalogs dataset-feed-head-sync control.flatsqldb
    fsql4-migrate.json source-metrics.db storefront.db peers.db
    trusted-import-lanes.json homepage.json)
# Preserve SQLite companions when a stopped database retains them.
for name in source-metrics.db storefront.db peers.db; do
    side_files+=("$name-wal" "$name-shm" "$name-journal")
done

log() {
    local line
    line="$(date -u +%Y-%m-%dT%H:%M:%SZ) $holder $1 token=$token ${2:-}"
    printf '%s\n' "$line" >> "$ledger"
    printf '%s\n' "$line" || true
}
fail() { log FAILED "step=$stage reason=$1"; exit 1; }
exists() { [[ -e $1 || -L $1 ]]; }

# Parse systemctl's quoted words without evaluating shell code or printing values.
# Unsupported escape spellings fail closed before the unit stops.
unit_env=()
parse_environment() {
    local input=$1 word= char quote= escaped=0 i
    for ((i=0; i<${#input}; i++)); do
        char=${input:i:1}
        if (( escaped )); then
            case $char in
                ' '|\"|\'|\\|'$'|'`') word+=$char ;;
                *) return 1 ;;
            esac
            escaped=0
        elif [[ $char == \\ ]]; then
            escaped=1
        elif [[ -n $quote ]]; then
            if [[ $char == "$quote" ]]; then quote=; else word+=$char; fi
        elif [[ $char == '"' || $char == "'" ]]; then
            quote=$char
        elif [[ $char == ' ' ]]; then
            if [[ -n $word ]]; then unit_env+=("$word"); word=; fi
        else
            word+=$char
        fi
    done
    [[ -z $quote && $escaped == 0 ]] || return 1
    [[ -z $word ]] || unit_env+=("$word")
    for word in "${unit_env[@]}"; do
        [[ $word =~ ^[a-zA-Z_][a-zA-Z0-9_]*= ]] || return 1
    done
}
stop_unit() {
    local end=$((SECONDS + 90)) pid state
    systemctl stop --no-block "$unit" || return 1
    while (( SECONDS < end )); do
        pid=$(systemctl show -p MainPID --value "$unit") || return 1
        state=$(systemctl show -p ActiveState --value "$unit") || return 1
        if [[ $pid == 0 && ( $state == inactive || $state == failed ) ]]; then return 0; fi
        sleep 1
    done
    return 1
}
wait_ready() {
    local end=$((SECONDS + ready_seconds)) status left
    while (( SECONDS < end )); do
        left=$((end - SECONDS))
        (( left <= 5 )) || left=5
        status=$(curl --silent --output /dev/null --write-out '%{http_code}' \
            --max-time "$left" -- "$ready_url") || status=000
        if [[ $status == 200 ]] && systemctl is-active --quiet "$unit"; then return 0; fi
        sleep 1
    done
    return 1
}
check_running_peer() {
    local invocation lines line found=
    invocation=$(systemctl show -p InvocationID --value "$unit") || return 1
    [[ $invocation =~ ^[a-f0-9]{32}$ ]] || return 1
    lines=$(journalctl --no-pager -o cat "_SYSTEMD_INVOCATION_ID=$invocation" \
        --grep='Peer ID: ') || return 1
    while IFS= read -r line; do
        if [[ $line =~ Peer\ ID:\ ([a-zA-Z0-9]+) ]]; then found=${BASH_REMATCH[1]}; fi
    done <<< "$lines"
    [[ $found == "$peer" ]]
}
stop_conversion() {
    [[ -n $conversion_pid ]] || return 0
    if kill -0 "$conversion_pid" 2>/dev/null; then
        # Inspect only this child's command line before signaling its exact PID.
        local arg matched=0
        while IFS= read -r -d '' arg; do
            [[ $arg != store-migrate ]] || matched=1
        done < "/proc/$conversion_pid/cmdline"
        (( matched )) || return 1
        kill -KILL "$conversion_pid" || return 1
    fi
    wait "$conversion_pid" 2>/dev/null || true
    conversion_pid=
}
rollback() {
    local after=0 name
    stage=STOP_CONVERSION
    stop_conversion || return 1
    exists "$root/fsql5" && after=1
    log ROLLBACK "after_switch=$after" || return 1
    stage=STOP_FOR_ROLLBACK
    stop_unit || return 1
    if (( after )); then
        stage=RESTORE_STORE
        [[ -d $backup && -f $root/fsql4/rollback-auxiliary.flatsqlmeta ]] || return 1
        mv -T -- "$root/fsql5" "$root/fsql5.failed-$stamp" || return 1
        cp -p -- "$root/fsql4/rollback-auxiliary.flatsqlmeta" "$root/auxiliary.flatsqlmeta" || return 1
        stage=RESTORE_SIDE_FILES
        mkdir -- "$backup/changed" || return 1
        for name in "${side_files[@]}"; do
            if exists "$root/$name"; then
                mv -T -- "$root/$name" "$backup/changed/$name" || return 1
            fi
            if exists "$backup/$name"; then
                cp -a -- "$backup/$name" "$root/$name" || return 1
            fi
        done
    fi
    stage=RESTORE_BUNDLE
    if exists "$old"; then
        if exists "$live"; then mv -T -- "$live" "$failed" || return 1; fi
        mv -T -- "$old" "$live" || return 1
    fi
    stage=START_OLD
    systemctl start --no-block "$unit" || return 1
    wait_ready || return 1
    stage=CHECK_OLD_IDENTITY
    check_running_peer || return 1
    if (( after )); then
        log ROLLED_BACK_AFTER_SWITCH "reason=$reason" || return 1
    else
        log ROLLED_BACK_BEFORE_SWITCH "reason=$reason" || return 1
    fi
}
finish() {
    local status=$? failed_step=$stage
    trap - EXIT
    trap '' TERM HUP INT PIPE
    (( done_ok )) && exit 0
    reason=${reason:-"step=$failed_step exit=$status"}
    if (( stopped )); then
        if ! rollback; then
            log ROLLBACK_FAILED "step=$stage reason=$reason" || true
        fi
    else
        log PRECHECK_FAILED "$reason" || true
    fi
    exit 1
}
trap finish EXIT
trap 'reason=SIGTERM; exit 143' TERM
trap 'reason=SIGHUP; exit 129' HUP
trap 'reason=SIGINT; exit 130' INT

log PRECHECK 'begin'
[[ $(systemctl show -p LoadState --value "$unit") == loaded ]] || fail unit_missing
systemctl is-active --quiet "$unit" || fail unit_inactive
user=$(systemctl show -p User --value "$unit")
[[ -z $user || $user == root ]] || fail unit_not_root
[[ -z $(systemctl show -p EnvironmentFiles --value "$unit") ]] || fail environment_files_not_supported
parse_environment "$(systemctl show -p Environment --value "$unit")" || fail environment_encoding
home_override=()
[[ $# == 12 ]] || home_override=("HOME=$node_home")
node_command=(env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin HOME="$node_home" "${unit_env[@]}" "${home_override[@]}")
[[ -d $live && ! -L $live && -d $staged && -f $config ]] || fail bundle_or_config_missing
[[ $staged != "$live" && $staged != "$live/"* && $live != "$staged/"* ]] || fail overlapping_bundles
[[ $root != "$base" && $root != "$base/"* && $base != "$root/"* ]] || fail overlapping_store
[[ $(stat -c %d "$staged") == "$(stat -c %d "$base")" ]] || fail different_filesystems
for path in "$old" "$failed" "$backup" "$root/fsql5.failed-$stamp"; do
    ! exists "$path" || fail recovery_path_exists
done
[[ -d $root/fsql4 && ! -L $root/fsql4 ]] || fail format4_missing
! exists "$root/fsql5" || fail format5_exists
[[ -f $root/auxiliary.flatsqlmeta && ! -L $root/auxiliary.flatsqlmeta ]] || fail auxiliary_missing
"${node_command[@]}" "$staged/bin/spacedatanetwork" version >/dev/null 2>&1 || fail staged_version
identity=$("${node_command[@]}" "$staged/bin/spacedatanetwork" show-identity -c "$config" 2>/dev/null) || fail identity_unreadable
[[ $identity == "$peer" ]] || fail identity_mismatch
check_running_peer || fail running_identity_mismatch
bytes=$(du -sb -- "$root/fsql4"); bytes=${bytes%%$'\t'*}
aux=$(stat -c %s -- "$root/auxiliary.flatsqlmeta")
free=$(df -B1 --output=avail -- "$root" | tail -n 1); free=${free// /}
need=$((3 * bytes + aux + 1073741824 + 5000000000))
log DISK "required_bytes=$need available_bytes=$free"
(( free >= need )) || fail disk_gate
log PRECHECK_OK
stage=STOP
stopped=1
stop_unit || fail stop_timeout
log STOPPED
stage=SWAP
mv -T -- "$live" "$old"
mv -T -- "$staged" "$live"
log SWAPPED
stage=BACKUP
mkdir -- "$backup"
for name in "${side_files[@]}"; do
    if exists "$root/$name"; then cp -a -- "$root/$name" "$backup/$name"; fi
done
log BACKED_UP
stage=CONVERT
# A child lets the shell handle signals while it supervises the foreground task.
signal_pending=
trap 'signal_pending=SIGTERM' TERM
trap 'signal_pending=SIGHUP' HUP
trap 'signal_pending=SIGINT' INT
"${node_command[@]}" "$live/bin/spacedatanetwork" store-migrate --to 5 --config "$config" --store "$root" &
conversion_pid=$!
trap 'reason=SIGTERM; exit 143' TERM
trap 'reason=SIGHUP; exit 129' HUP
trap 'reason=SIGINT; exit 130' INT
if [[ -n $signal_pending ]]; then reason=$signal_pending; exit 1; fi
log CONVERTING "pid=$conversion_pid budget_minutes=$minutes"
deadline=$((SECONDS + minutes * 60))
while kill -0 "$conversion_pid" 2>/dev/null; do
    (( SECONDS < deadline )) || fail conversion_budget
    sleep 1
done
wait "$conversion_pid" || fail conversion_exit
conversion_pid=
stage=CHECK_SWITCH
marker=$(od -An -tx1 -j4 -N2 -- "$root/fsql5/STORE"); marker=${marker// /}
[[ $marker == 0500 && -d $root/fsql4 && -f $root/fsql4/rollback-auxiliary.flatsqlmeta ]] || fail invalid_switch
log SWITCHED
stage=START_NEW
systemctl start --no-block "$unit"
wait_ready || fail ready_budget
stage=CHECK_NEW_IDENTITY
check_running_peer || fail running_identity_mismatch
log SUCCESS 'format=5'
done_ok=1
