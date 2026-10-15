#!/usr/bin/env python3
"""sec001-node-check.py — OWNER-RUN on each fleet node (SEC-001 rotation).

Read-only unless --apply. Python 3 standard library only, so it runs on the
hosts (python3 is already a deploy precondition there) and on the Mac nodes.

  # what is running, the compromise check, and the current update roots
  sudo python3 -I sec001-node-check.py --unit space-data-network-module-delivery.service
  python3 -I sec001-node-check.py --unit spacedatanetwork.service --user          # vm-orbit-det-01
  python3 -I sec001-node-check.py --bundle <bundle root>                          # a Mac node

  # the out-of-band update-root swap (ROTATION.md step 3)
  sudo python3 -I sec001-node-check.py --unit <unit> --swap-roots update-roots.json \\
       --evidence-dir /root/sec001-evidence --apply

What it reports (deployment/identity/ROTATION.md steps 0 and 0b):
  - the running binary's path (from the unit's MainPID, never a hard-coded
    path) and its sha256, against the values recorded at containment;
  - the bundle root, the trust-roots file it reads (honouring an
    SDN_UPDATE_TRUST_ROOTS in the unit's environment) and the key ids in it;
  - every deploy-ledger line and every updates/{staged,incoming,rollback,
    failed} entry recorded since 2026-07-30 (the day the leaked node key became
    the update root), with update_id / version / signer — so the owner can mark
    each as published-by-us or not. It LISTS; it never deletes.

--swap-roots <file> --apply replaces the trust-roots file with <file> after
checking it: a JSON object of {key_id: base64 Ed25519 SPKI}, each key id the
first 12 hex of sha256 over its key, none of them the published root
(d4a971a7e534). The old file is copied to --evidence-dir first, the new one is
written atomically with the old owner and mode, and read back by hash. It does
not restart anything; ROTATION.md says when.
"""
import argparse
import base64
import datetime
import hashlib
import json
import os
import platform
import shutil
import subprocess
import sys
import tempfile

LEAKED_ROOT_KEY_ID = "d4a971a7e534"
EXPOSED_SINCE = "2026-07-30"
SPKI_PREFIX = bytes.fromhex("302a300506032b6570032100")
# Recorded at containment, 2026-10-15 13:13-13:25Z (ROTATION.md step 0).
RECORDED_SHA256 = {
    "5f2ae665276463b1295416ddf942cd4819ed26209ceec111bb9a656a045bdece": "host-01 / host-02: bundle 1.0.6-updatelane.24c0512bc (installed 2026-10-09)",
    "4c3c82a630a911568e88f1dd9a2a18886332751d9a45bf61d3cf50a785ce2b78": "vm-orbit-det-01",
}
UPDATE_SUBDIRS = ("staged", "incoming", "rollback", "failed")
SYSTEM_LEDGERS = ("/var/log/sdn-deploy-lock.ledger", "/opt/spacedatanetwork/deploy-ledger.log")


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def systemctl_show(unit, prop, user):
    cmd = ["systemctl"] + (["--user"] if user else []) + ["show", "-p", prop, "--value", unit]
    return subprocess.run(cmd, capture_output=True, text=True, check=True).stdout.strip()


def parse_env_file(path):
    """A systemd EnvironmentFile: KEY=VALUE lines, # comments, optional quotes."""
    env = {}
    try:
        with open(path, "r", errors="replace") as f:
            for line in f:
                line = line.strip()
                if not line or line.startswith(("#", ";")) or "=" not in line:
                    continue
                k, v = line.split("=", 1)
                k = k.strip()
                if k.startswith("export "):
                    k = k[7:].strip()
                v = v.strip()
                if len(v) >= 2 and v[0] == v[-1] and v[0] in "\"'":
                    v = v[1:-1]
                env[k] = v
    except OSError:
        pass
    return env


def unit_environment(unit, user, pid):
    """The environment the daemon actually runs with (verifier r2 R2): the
    process's own /proc/<pid>/environ when readable, else the unit's
    Environment= merged over its EnvironmentFile= files, in systemd's order."""
    try:
        with open(f"/proc/{pid}/environ", "rb") as f:
            items = f.read().split(b"\0")
        return {k.decode(errors="replace"): v.decode(errors="replace") for k, _, v in (i.partition(b"=") for i in items if b"=" in i)}, f"/proc/{pid}/environ"
    except OSError:
        pass
    env = {}
    for entry in systemctl_show(unit, "EnvironmentFiles", user).splitlines():
        path = entry.split(" (", 1)[0].strip()
        if path:
            env.update(parse_env_file(path.lstrip("-")))
    for item in systemctl_show(unit, "Environment", user).split():
        if "=" in item:
            k, v = item.split("=", 1)
            env[k] = v
    return env, "unit Environment= and EnvironmentFile="


def running_binary(unit, user):
    pid = systemctl_show(unit, "MainPID", user)
    if not pid or pid == "0":
        raise SystemExit(f"{unit} is not running (MainPID {pid or 'unset'}); start it or pass --bundle")
    exe = os.path.realpath(f"/proc/{pid}/exe")
    env, source = unit_environment(unit, user, pid)
    return int(pid), exe, env, source


def bundle_root_of(exe):
    d = os.path.dirname(exe)
    for _ in range(4):
        if os.path.isfile(os.path.join(d, "manifest.json")):
            return d
        d = os.path.dirname(d)
    return os.path.dirname(os.path.dirname(exe))


def root_key_id(encoded):
    raw = base64.b64decode(encoded, validate=True)
    if len(raw) == 44 and raw[:12] == SPKI_PREFIX:
        raw = raw[12:]
    if len(raw) != 32:
        raise ValueError("not an Ed25519 key")
    return hashlib.sha256(raw).hexdigest()[:12]


def anchored_roots(anchors_path):
    """deployment/identity/anchors.json updateRoots: the ONLY roots a node may hold."""
    anchors = json.load(open(anchors_path))
    roots = anchors.get("updateRoots") or {}
    if not roots or any(k.startswith("PLACEHOLDER-") or str(v).startswith("PLACEHOLDER-") for k, v in roots.items()):
        raise SystemExit(f"REFUSED: {anchors_path} has no released update root yet (placeholders); land the owner's anchors first")
    check_roots(roots)
    return roots


def check_roots(roots, required=None):
    if not isinstance(roots, dict) or not roots:
        raise SystemExit("REFUSED: the roots file must be a non-empty JSON object {key_id: SPKI}")
    for key_id, encoded in roots.items():
        if key_id.lower() == LEAKED_ROOT_KEY_ID or str(encoded).startswith("PLACEHOLDER-") or key_id.startswith("PLACEHOLDER-"):
            raise SystemExit(f"REFUSED: the roots file names {key_id}, the published root or a placeholder")
        try:
            computed = root_key_id(encoded)
        except Exception as error:  # noqa: BLE001
            raise SystemExit(f"REFUSED: root {key_id}: {error}")
        if computed == LEAKED_ROOT_KEY_ID:
            raise SystemExit("REFUSED: the roots file carries the published root's key under another id")
        if computed != key_id:
            raise SystemExit(f"REFUSED: key {computed} is listed under key id {key_id}")
    # Not merely "not the leaked root": exactly the committed one (verifier r2 R2).
    if required is not None and roots != required:
        raise SystemExit(
            "REFUSED: the roots file is not exactly anchors.json updateRoots "
            f"(anchored: {', '.join(sorted(required))}; given: {', '.join(sorted(roots))})"
        )


def iso_at_or_after(value, since):
    return isinstance(value, str) and value[:10] >= since


def mtime_iso(path):
    return datetime.datetime.fromtimestamp(os.path.getmtime(path), datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def list_ledger(path, since, out):
    if not os.path.exists(path):
        out.append(f"  {path}: absent")
        return
    out.append(f"  {path}:")
    shown = 0
    with open(path, "r", errors="replace") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                e = json.loads(line)
            except ValueError:
                if line[:10] >= since or since in line:
                    out.append(f"    (text) {line[:200]}")
                    shown += 1
                continue
            if iso_at_or_after(e.get("recorded_at", ""), since):
                out.append(
                    "    {recorded_at} {action:<10} {update_id} version={version} seq={sequence} from={fv}".format(
                        recorded_at=e.get("recorded_at"), action=str(e.get("action")), update_id=e.get("update_id"),
                        version=e.get("version"), sequence=e.get("sequence"), fv=e.get("from_version", "")))
                shown += 1
    if not shown:
        out.append(f"    nothing recorded since {since}")


def list_updates(bundle, since, out):
    updates = os.path.join(bundle, "updates")
    out.append(f"  {updates}:")
    if not os.path.isdir(updates):
        out.append("    absent")
        return
    for sub in UPDATE_SUBDIRS:
        d = os.path.join(updates, sub)
        if not os.path.isdir(d):
            continue
        for name in sorted(os.listdir(d)):
            entry = os.path.join(d, name)
            when = mtime_iso(entry)
            manifest = os.path.join(entry, "manifest.json") if os.path.isdir(entry) else ""
            info = ""
            if manifest and os.path.isfile(manifest):
                try:
                    m = json.load(open(manifest))
                    info = "update_id={} version={} seq={} signer={} created={} source_commit={}".format(
                        m.get("update_id"), m.get("version"), m.get("sequence"), (m.get("signing") or {}).get("key_id"),
                        m.get("created_at"), (m.get("provenance") or {}).get("source_commit", ""))
                except ValueError:
                    info = "manifest.json unreadable"
            flag = "SINCE-EXPOSURE" if when[:10] >= since else "older"
            out.append(f"    [{flag}] {sub}/{name} mtime={when} {info}")
    state = os.path.join(updates, "state.json")
    if os.path.isfile(state):
        try:
            s = json.load(open(state))
            out.append(f"    state.json: {json.dumps(s, sort_keys=True)[:300]}")
        except ValueError:
            out.append("    state.json unreadable")


def swap_roots(trust_path, new_roots_path, evidence_dir, required):
    new_bytes = open(new_roots_path, "rb").read()
    check_roots(json.loads(new_bytes), required)
    os.makedirs(evidence_dir, mode=0o700, exist_ok=True)
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    if os.path.exists(trust_path):
        st = os.stat(trust_path)
        saved = os.path.join(evidence_dir, f"update-roots.json.before-sec001-{stamp}")
        shutil.copy2(trust_path, saved)
        mode, uid, gid = st.st_mode & 0o777, st.st_uid, st.st_gid
    else:
        saved = ""
        os.makedirs(os.path.dirname(trust_path), exist_ok=True)
        mode, uid, gid = 0o644, os.getuid(), os.getgid()
    fd, tmp = tempfile.mkstemp(dir=os.path.dirname(trust_path), prefix=".update-roots.")
    try:
        with os.fdopen(fd, "wb") as f:
            f.write(new_bytes)
            f.flush()
            os.fsync(f.fileno())
        os.chmod(tmp, mode)
        try:
            os.chown(tmp, uid, gid)
        except PermissionError:
            pass
        os.replace(tmp, trust_path)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)
    if hashlib.sha256(open(trust_path, "rb").read()).hexdigest() != hashlib.sha256(new_bytes).hexdigest():
        raise SystemExit(f"FAILED: {trust_path} does not read back as the new roots")
    return saved


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    where = p.add_mutually_exclusive_group(required=True)
    where.add_argument("--unit", help="systemd unit running the node (path and environment are read from it)")
    where.add_argument("--bundle", help="bundle root, for a node without a unit (Mac nodes)")
    p.add_argument("--user", action="store_true", help="the unit is a systemd --user unit")
    p.add_argument("--binary", help="with --bundle: the running binary (default <bundle>/bin/spacedatanetwork)")
    p.add_argument("--since", default=EXPOSED_SINCE, help=f"list changes on or after this date (default {EXPOSED_SINCE})")
    p.add_argument("--swap-roots", metavar="FILE", help="replace the trust-roots file with FILE (needs --anchors; --apply to install)")
    p.add_argument("--anchors", metavar="ANCHORS_JSON", help="deployment/identity/anchors.json: the roots must equal its updateRoots exactly")
    p.add_argument("--evidence-dir", default="", help="where the old roots file is copied before a swap")
    p.add_argument("--apply", action="store_true", help="actually perform --swap-roots")
    p.add_argument("--expect-sha256", action="append", default=[], metavar="HEX",
                   help="another binary sha256 you recorded (e.g. host-01's SEC-001 hotfix build, live since 15:11Z); repeatable")
    args = p.parse_args(argv)

    out = [f"SEC-001 node check on {platform.node()} at {datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')}"]
    env = {}
    env_source = ""
    if args.unit:
        pid, exe, env, env_source = running_binary(args.unit, args.user)
        out.append(f"unit {args.unit}: MainPID {pid}, binary {exe}; environment from {env_source}")
        bundle = bundle_root_of(exe)
    else:
        bundle = os.path.realpath(args.bundle)
        exe = os.path.realpath(args.binary or os.path.join(bundle, "bin", "spacedatanetwork"))
        out.append(f"bundle {bundle}, binary {exe}")
    digest = sha256_file(exe) if os.path.isfile(exe) else "(binary not found)"
    recorded = dict(RECORDED_SHA256)
    for value in args.expect_sha256:
        recorded[value.strip().lower()] = "a value passed with --expect-sha256"
    known = recorded.get(digest)
    out.append(f"binary sha256 {digest}  -> {'matches the recorded ' + known if known else 'NOT one of the values recorded at containment'}")
    out.append(f"bundle root {bundle}")

    trust_path = env.get("SDN_UPDATE_TRUST_ROOTS") or os.path.join(bundle, "trust", "update-roots.json")
    out.append(f"trust roots file {trust_path}{' (from SDN_UPDATE_TRUST_ROOTS)' if 'SDN_UPDATE_TRUST_ROOTS' in env else ''}")
    if os.path.isfile(trust_path):
        try:
            roots = json.load(open(trust_path))
            ids = sorted(roots)
            leaked = [i for i in ids if i.lower() == LEAKED_ROOT_KEY_ID]
            out.append(f"  key ids: {', '.join(ids)}{'   <- still trusts the PUBLISHED root' if leaked else ''}")
            if args.anchors:
                wanted = anchored_roots(args.anchors)
                out.append(f"  {'MATCHES' if roots == wanted else 'DOES NOT MATCH'} anchors.json updateRoots ({', '.join(sorted(wanted))})")
            out.append(f"  sha256 {sha256_file(trust_path)}")
        except ValueError:
            out.append("  unreadable JSON")
    else:
        out.append("  absent")

    out.append(f"deploy ledgers, entries recorded since {args.since}:")
    list_ledger(os.path.join(bundle, "deploy-ledger.jsonl"), args.since, out)
    for path in SYSTEM_LEDGERS:
        list_ledger(path, args.since, out)
    out.append(f"update working tree, entries since {args.since} flagged:")
    list_updates(bundle, args.since, out)
    print("\n".join(out))

    if args.swap_roots:
        if not args.anchors:
            raise SystemExit("REFUSED: --swap-roots needs --anchors <anchors.json>: the new roots must equal its updateRoots exactly")
        required = anchored_roots(args.anchors)
        if not args.apply:
            check_roots(json.load(open(args.swap_roots)), required)
            print(f"\n--swap-roots: {args.swap_roots} is valid; re-run with --apply (and --evidence-dir) to install it at {trust_path}")
            return 0
        if not args.evidence_dir:
            raise SystemExit("REFUSED: --apply needs --evidence-dir for the old roots file")
        saved = swap_roots(trust_path, args.swap_roots, args.evidence_dir, required)
        print(f"\nswapped {trust_path}: now {', '.join(sorted(json.load(open(trust_path))))}" + (f"; old file kept at {saved}" if saved else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main())
