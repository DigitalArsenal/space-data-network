#!/usr/bin/env python3
"""Isolated systemd rehearsal. Inputs and retained evidence are explicit paths."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile

CASES = ('forward', 'interrupted', 'budget', 'after-switch', 'wrong-peer',
         'disk', 'script-term', 'script-hup')


def verify(archive, checksums):
    digest = hashlib.file_digest(open(archive, 'rb'), 'sha256').hexdigest()
    expected = [line.split()[0] for line in checksums.read_text().splitlines()
                if len(line.split()) == 2 and line.split()[1].lstrip('*') == archive.name]
    if expected != [digest]:
        raise ValueError('release checksum mismatch: ' + archive.name)
    return {'archive': archive.name, 'sha256': digest}


def unpack(archive, destination):
    destination.mkdir(parents=True)
    with tarfile.open(archive) as bundle:
        members = bundle.getmembers()
        tops = {m.name.split('/')[0] for m in members if m.name}
        if len(tops) != 1:
            raise ValueError('archive needs one top-level bundle directory')
        bundle.extractall(destination.parent / (destination.name + '.unpack'), filter='data')
    source = destination.parent / (destination.name + '.unpack') / tops.pop()
    for child in source.iterdir():
        shutil.move(str(child), destination / child.name)
    source.rmdir()
    source.parent.rmdir()
    if not (destination / 'bin/spacedatanetwork').is_file():
        raise ValueError('archive has no bundle launcher')


def ignore_private(directory, names):
    return [n for n in names if n in ('keys', 'secrets', 'auth.db') or 'udl' in n.lower()]


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    for name in ('new-archive', 'new-checksums',
                 'work-dir', 'record-evidence-dir'):
        ap.add_argument('--' + name, required=True, type=Path)
    ap.add_argument('--cache-dir', type=Path, help='shared engine cache; contains no identities')
    ap.add_argument('--store', required=True, action='append', type=Path)
    ap.add_argument('--old-binary', required=True, action='append', type=Path)
    ap.add_argument('--old-sha256', required=True, action='append')
    ap.add_argument('--arch', choices=('arm64', 'amd64'), default='amd64')
    ap.add_argument('--case', choices=CASES, action='append')
    ap.add_argument('--conversion-minutes', type=int, default=90)
    ap.add_argument('--ready-seconds', type=int, default=300)
    args = ap.parse_args()
    if len(args.old_binary) != len(args.store) or len(args.old_sha256) != len(args.store):
        ap.error('each store requires its matching old binary and SHA-256')
    here = Path(__file__).resolve().parent
    work = args.work_dir.resolve()
    work.mkdir(parents=True, exist_ok=False)
    cache = (args.cache_dir or (work / 'cache')).resolve()
    cache.mkdir(parents=True, exist_ok=True)
    results = []
    active = None
    image = 'sdn-format5-rehearsal:' + str(os.getpid())
    try:
        verified = [verify(args.new_archive, args.new_checksums)]
        for binary, expected in zip(args.old_binary, args.old_sha256):
            with binary.open('rb') as stream:
                digest = hashlib.file_digest(stream, 'sha256').hexdigest()
            if digest != expected:
                raise ValueError('old binary checksum mismatch')
            verified.append({'old_binary': binary.name, 'sha256': digest})
        (work / 'archives.json').write_text(json.dumps(verified, indent=2) + '\n')
        context = work / 'image'
        context.mkdir(exist_ok=False)
        shutil.copy2(here / 'Dockerfile', context / 'Dockerfile')
        with open(work / 'image.log', 'w') as log:
            subprocess.run(['docker', 'build', '--platform=linux/' + args.arch,
                            '-t', image, str(context)], stdout=log, stderr=subprocess.STDOUT, check=True)
        for number, source in enumerate(args.store, 1):
            for case in args.case or CASES:
                directory = work / ('store-' + str(number)) / case
                directory.mkdir(parents=True)
                shutil.copytree(source, directory / 'store', ignore=ignore_private)
                old_bundle = directory / 'base/sdn-bundle'
                (old_bundle / 'bin').mkdir(parents=True)
                old_binary = args.old_binary[number - 1]
                shutil.copy2(old_binary, old_bundle / 'bin/spacedatanetwork')
                shutil.copy2(old_binary.parent / 'manifest.json', old_bundle / 'manifest.json')
                unpack(args.new_archive, directory / 'base/staged')
                shutil.copy2(here.parent / 'roll-to-format5.sh', directory / 'roll.sh')
                shutil.copy2(here / 'case.py', directory / 'case.py')
                shutil.copy2(here / 'records.py', directory / 'records.py')
                evidence = args.record_evidence_dir.resolve() / ('store-' + str(number)) / case
                evidence.mkdir(parents=True, exist_ok=False)
                active = 'sdn-format5-' + str(os.getpid()) + '-' + str(number) + '-' + case
                with open(directory / 'container.log', 'w') as log:
                    subprocess.run(['docker', 'run', '-d', '-t', '--name', active, '--hostname=sdn-rehearsal', '--privileged',
                                    '--cgroupns=host', '--network=none', '--platform=linux/' + args.arch,
                                    '--cpus=2', '--memory=6g', '--tmpfs=/run', '--tmpfs=/run/lock',
                                    '--tmpfs=/tmp', '-v', '/sys/fs/cgroup:/sys/fs/cgroup:rw',
                                    '-v', str(cache) + ':/engine-cache',
                                    '-v', str(evidence) + ':/evidence',
                                    '-v', str(directory) + ':/case', image],
                                   stdout=log, stderr=subprocess.STDOUT, check=True)
                try:
                    with open(directory / 'driver.log', 'w') as log:
                        status = subprocess.run(['docker', 'exec', active, 'python3', '/case/case.py',
                                                 case, str(args.conversion_minutes), str(args.ready_seconds)],
                                                stdout=log, stderr=subprocess.STDOUT).returncode
                    (directory / 'driver.exit').write_text(str(status) + '\n')
                    result_file = directory / 'result.json'
                    result = json.loads(result_file.read_text()) if result_file.exists() else {
                        'status': 'failed', 'reason': 'driver exited without a result'}
                    results.append({'store': number, 'case': case, **result})
                finally:
                    with open(directory / 'journal.log', 'w') as log:
                        subprocess.run(['docker', 'exec', active, 'journalctl', '--no-pager', '-o', 'short-iso'],
                                       stdout=log, stderr=subprocess.STDOUT)
                    # Docker addresses only the container created above.
                    subprocess.run(['docker', 'stop', active], check=True, stdout=subprocess.DEVNULL)
                    subprocess.run(['docker', 'rm', active], check=True, stdout=subprocess.DEVNULL)
                    active = None
                    if not (directory / 'driver.exit').exists() or not (directory / 'result.json').exists():
                        raise RuntimeError('incomplete case retained; inspect before creating another store')
                    # Retain evidence, then reclaim this completed case before another copy.
                    for name in ('store', 'store-source', 'base'):
                        if (directory / name).exists():
                            shutil.rmtree(directory / name)
                    for backup in directory.glob('store.roll-backup-*'):
                        for log in backup.glob('conversion-attempt-*.log'):
                            shutil.copy2(log, directory / log.name)
                        shutil.rmtree(backup)
                    (directory / 'disk.img').unlink(missing_ok=True)
                (work / 'results.json').write_text(json.dumps(results, indent=2) + '\n')
    except Exception as error:
        (work / 'blocked.txt').write_text(str(error) + '\n')
        raise
    finally:
        # Logs and record evidence survive cleanup; interrupted cases remain inspectable.
        if active:
            subprocess.run(['docker', 'stop', active], stdout=subprocess.DEVNULL)
        (work / 'results.json').write_text(json.dumps(results, indent=2) + '\n')
    return int(any(r['status'] != 'passed' for r in results))


if __name__ == '__main__':
    sys.exit(main())
