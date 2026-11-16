#!/usr/bin/env python3
"""Run inside an isolated container whose PID 1 is systemd."""
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import shutil
import subprocess
import sys
import time
import urllib.request

ROOT = Path('/case')
UNIT = 'rehearsal-node.service'
READY = 'http://127.0.0.1:5001/api/v1/ready'
BIN = ROOT / 'base/sdn-bundle/bin/spacedatanetwork'


def command(*args, check=True):
    return subprocess.run(args, capture_output=True, text=True, check=check).stdout.strip()


def wait_ready(seconds):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        try:
            with urllib.request.urlopen(READY, timeout=min(3, max(0.1, end-time.monotonic()))) as r:
                if r.status == 200:
                    return
        except Exception:
            time.sleep(1)
    raise RuntimeError('ready budget exceeded')


def hashes():
    result = {}
    for p in sorted((ROOT / 'store/fsql4').rglob('*')):
        if p.is_file():
            with p.open('rb') as f:
                digest = hashlib.sha256()
                for block in iter(lambda: f.read(1024 * 1024), b''):
                    digest.update(block)
                result[str(p.relative_to(ROOT / 'store/fsql4'))] = digest.hexdigest()
    return result


def counts():
    with urllib.request.urlopen('http://127.0.0.1:5001/api/v1/stats', timeout=60) as r:
        data = json.load(r)
    # Persist only aggregates, never source rows or records.
    result = {s['schema']: s['count'] for s in data['schemas']}
    if not result or not sum(result.values()):
        raise RuntimeError('public stats has no populated per-standard counts')
    return result


def peer():
    invocation = command('systemctl', 'show', '-p', 'InvocationID', '--value', UNIT)
    journal = command('journalctl', '--no-pager', '-o', 'cat',
                      '_SYSTEMD_INVOCATION_ID=' + invocation, '--grep=Peer ID: ')
    matches = re.findall(r'Peer ID: ([a-zA-Z0-9]+)', journal)
    if not matches:
        raise RuntimeError('running node has no journal identity')
    return matches[-1]


def tree_size(path):
    return sum(p.stat().st_size for p in path.rglob('*') if p.is_file()) if path.exists() else 0


def run(case, minutes, seconds):
    if Path('/proc/1/comm').read_text().strip() != 'systemd':
        raise RuntimeError('PID 1 is not systemd')
    if case == 'disk':
        # A real sparse loop filesystem bounds free space without filling a host disk.
        source = ROOT / 'store-source'
        (ROOT / 'store').rename(source)
        (ROOT / 'store').mkdir()
        image = ROOT / 'disk.img'
        size = tree_size(source) + 2 * 1024**3
        command('truncate', '-s', str(size), str(image))
        command('mkfs.ext4', '-q', '-F', '-m', '0', str(image))
        command('mount', '-o', 'loop', str(image), str(ROOT / 'store'))
        shutil.copytree(source, ROOT / 'store', dirs_exist_ok=True)
        (ROOT / 'disk-filesystem.json').write_text(json.dumps({
            'filesystem': 'ext4', 'capacity_bytes': size}) + '\n')
    (ROOT / 'home').mkdir(exist_ok=True)
    password = ROOT / 'test-password'
    if not password.exists():
        password.write_text(os.urandom(32).hex())
    password.chmod(0o600)
    os.environ['SDN_KEY_PASSWORD_FILE'] = str(password)
    os.environ['XDG_CACHE_HOME'] = '/engine-cache'
    os.environ['HOME'] = '/case/home'
    # Empty bootstrap lists trigger built-in public defaults in these releases.
    # A synthetic identity at an unused loopback port prevents that fallback.
    alphabet = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz'
    value = int.from_bytes(bytes.fromhex('002408011220') + bytes([1]) * 32, 'big')
    encoded = ''
    while value:
        value, digit = divmod(value, 58)
        encoded = alphabet[digit] + encoded
    isolated_peer = '1' + encoded
    # All keys belong to this isolated test. The container has no external network.
    (ROOT / 'config.yaml').write_text('''mode: full
flows:
  services: []
network:
  listen: [/ip4/127.0.0.1/tcp/4001]
  bootstrap: [/ip4/127.0.0.1/tcp/9/p2p/ISOLATED_PEER]
  edge_relays: []
  enable_relay: never
storage:
  path: /case/store
  max_size: 200GB
  gc_interval: 24h
peers:
  trusted_peers: []
  registry_path: /case/empty-peer-registry.json
  enable_dht: false
  enable_mdns: false
tor:
  enabled: false
admin:
  enabled: true
  listen_addr: 127.0.0.1:5001
  tls_mode: disabled
  require_auth: false
setup:
  data_path: /case/store
'''.replace('ISOLATED_PEER', isolated_peer))
    unit = Path('/etc/systemd/system') / UNIT
    unit.write_text('''[Unit]
Description=Isolated store rehearsal
[Service]
Type=simple
User=root
Environment=HOME=/case/home XDG_CACHE_HOME=/engine-cache SDN_KEY_PASSWORD_FILE=/case/test-password
ExecStart=/case/base/sdn-bundle/bin/spacedatanetwork daemon --config /case/config.yaml
Restart=no
TimeoutStopSec=75
''')
    command('systemctl', 'daemon-reload')
    command('systemctl', 'start', '--no-block', UNIT)
    wait_ready(seconds)
    command('systemctl', 'is-active', '--quiet', UNIT)
    (ROOT / 'baseline.json').write_text(json.dumps({'ready': True, 'http_status': 200}) + '\n')
    expected = peer()
    before = counts()
    (ROOT / 'counts-before.json').write_text(json.dumps(before, sort_keys=True) + '\n')
    before_hashes = hashes()
    (ROOT / 'hashes-before.json').write_text(json.dumps(before_hashes, sort_keys=True) + '\n')
    # Populate each release's engine cache before the outage.
    with open(ROOT / 'prewarm.log', 'w') as log:
        subprocess.run(['/case/base/staged/bin/spacedatanetwork', 'prewarm-aot', '--config',
                        '/case/config.yaml'], stdout=log, stderr=subprocess.STDOUT, check=True)
    if case == 'after-switch':
        drop = Path('/etc/systemd/system') / (UNIT + '.d')
        drop.mkdir()
        (drop / 'fail-new.conf').write_text('''[Service]
ExecStartPre=/bin/sh -c 'test ! -d /case/store/fsql5'
''')
        command('systemctl', 'daemon-reload')
    if case == 'disk':
        # Verify that the actual filesystem fails the production space formula.
        vfs = os.statvfs(ROOT / 'store')
        available = vfs.f_bavail * vfs.f_frsize
        required = 3 * tree_size(ROOT / 'store/fsql4') + (ROOT / 'store/auxiliary.flatsqlmeta').stat().st_size + 1073741824 + 5000000000
        (ROOT / 'disk.json').write_text(json.dumps({'available_bytes': available, 'required_bytes': required}) + '\n')
        if available >= required:
            return {'status': 'not run', 'reason': 'work filesystem has enough free space; supply a constrained filesystem'}
    args = ['/bin/bash', '/case/roll.sh', UNIT, '/case/base', '/case/base/staged',
            '/case/config.yaml', '/case/store', READY,
            'WrongExpectedPeer' if case == 'wrong-peer' else expected,
            '/case/ledger.log', 'rehearsal', case, '1' if case == 'budget' else str(minutes),
            str(seconds), '/case/home']
    baseline_pid = command('systemctl', 'show', '-p', 'MainPID', '--value', UNIT)
    start = time.monotonic()
    proc = subprocess.Popen(['systemd-run', '--unit=rehearsal-roll', '--collect', '--wait', '--pipe', *args],
                            stdout=open(ROOT / 'roll.log', 'w'), stderr=subprocess.STDOUT)
    injected = False
    peak_kib = 0
    migration_start = None
    migration_end = None
    while proc.poll() is None:
        ledger = (ROOT / 'ledger.log').read_text() if (ROOT / 'ledger.log').exists() else ''
        match = re.search(r' CONVERTING .*pid=(\d+)', ledger)
        if match:
            pid = int(match[1])
            migration_start = migration_start or time.monotonic()
            stat = Path('/proc') / str(pid) / 'status'
            if stat.exists():
                rss = re.search(r'^VmRSS:\s+(\d+)', stat.read_text(), re.M)
                if rss:
                    peak_kib = max(peak_kib, int(rss[1]))
            pending = ROOT / 'store/.store-format5-pending'
            if not injected and pending.exists() and time.monotonic() - migration_start >= 10:
                if case in ('interrupted', 'script-term', 'script-hup'):
                    if case == 'interrupted':
                        command_line = (Path('/proc') / str(pid) / 'cmdline').read_bytes().split(b'\0')
                        if b'store-migrate' not in command_line:
                            raise RuntimeError('conversion PID no longer matches')
                        os.kill(pid, signal.SIGTERM)
                    else:
                        script_pid = int(command('systemctl', 'show', '-p', 'MainPID', '--value', 'rehearsal-roll'))
                        command_line = (Path('/proc') / str(script_pid) / 'cmdline').read_bytes()
                        if b'/case/roll.sh' not in command_line:
                            raise RuntimeError('script PID no longer matches')
                        os.kill(script_pid, signal.SIGTERM if case == 'script-term' else signal.SIGHUP)
                    (ROOT / 'injected.json').write_text(json.dumps({'case': case, 'conversion_pid': pid, 'seconds': time.monotonic()-start}) + '\n')
                    injected = True
        if ' SWITCHED ' in ledger and migration_end is None:
            migration_end = time.monotonic()
        time.sleep(0.25)
    code = proc.wait()
    (ROOT / 'roll.exit').write_text(str(code) + '\n')
    ledger = (ROOT / 'ledger.log').read_text()
    wait_ready(seconds)
    if peer() != expected:
        raise RuntimeError('identity changed')
    after = counts()
    (ROOT / 'counts-after.json').write_text(json.dumps(after, sort_keys=True) + '\n')
    (ROOT / 'observations.json').write_text(json.dumps({
        'ready': True, 'running_peer_unchanged': True, 'counts_equal': before == after,
        'roll_exit': code, 'injected': injected}) + '\n')
    if any(' ' + event + ' ' in ledger for event in
           ('SUCCESS', 'ROLLED_BACK_BEFORE_SWITCH', 'ROLLED_BACK_AFTER_SWITCH')):
        replay_pid = command('systemctl', 'show', '-p', 'MainPID', '--value', UNIT)
        with open(ROOT / 'second-run.log', 'w') as log:
            second = subprocess.run(args, stdout=log, stderr=subprocess.STDOUT).returncode
        (ROOT / 'second-run.exit').write_text(str(second) + '\n')
        unchanged = command('systemctl', 'show', '-p', 'MainPID', '--value', UNIT) == replay_pid
        (ROOT / 'second-run.json').write_text(json.dumps({
            'refused': second != 0, 'daemon_pid_unchanged': unchanged}) + '\n')
        if second == 0 or not unchanged:
            raise RuntimeError('second run was not refused without restarting the node')
    if before != after:
        raise RuntimeError('per-standard counts changed')
    if case == 'forward':
        if code != 0 or ' SUCCESS ' not in ledger:
            raise RuntimeError('forward conversion failed')
    elif case in ('wrong-peer', 'disk'):
        if code == 0 or ' STOPPED ' in ledger or ' PRECHECK_FAILED ' not in ledger:
            raise RuntimeError('precheck did not refuse before stop')
        if command('systemctl', 'show', '-p', 'MainPID', '--value', UNIT) != baseline_pid:
            raise RuntimeError('precheck changed the running daemon')
        if before_hashes != hashes():
            raise RuntimeError('precheck changed format 4')
        if not (ROOT / 'base/staged').exists() or (ROOT / 'store/fsql5').exists():
            raise RuntimeError('precheck changed bundle or store selection')
    else:
        event = 'ROLLED_BACK_AFTER_SWITCH' if case == 'after-switch' else 'ROLLED_BACK_BEFORE_SWITCH'
        if code == 0 or (' ' + event + ' ') not in ledger:
            raise RuntimeError('expected automatic rollback did not complete')
        if case in ('interrupted', 'script-term', 'script-hup') and not injected:
            raise RuntimeError('failure was not injected during conversion')
        if case == 'budget' and 'reason=conversion_budget' not in ledger:
            raise RuntimeError('conversion did not reach its budget')
        after_hashes = hashes()
        (ROOT / 'hashes-after.json').write_text(json.dumps(after_hashes, sort_keys=True) + '\n')
        if case != 'after-switch' and before_hashes != after_hashes:
            raise RuntimeError('format-4 file hashes changed')
    return {'status': 'passed', 'roll_exit': code, 'seconds': round(time.monotonic()-start, 2),
            'conversion_seconds': round(migration_end-migration_start, 2) if migration_end else None,
            'peak_conversion_rss_kib_sampled': peak_kib,
            'fsql4_bytes': tree_size(ROOT / 'store/fsql4'),
            'fsql5_bytes': tree_size(ROOT / 'store/fsql5'), 'counts': after}


if __name__ == '__main__':
    try:
        result = run(sys.argv[1], int(sys.argv[2]), int(sys.argv[3]))
    except Exception as error:
        result = {'status': 'failed', 'reason': str(error)}
    (ROOT / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
    sys.exit(0 if result['status'] == 'passed' else 1)
