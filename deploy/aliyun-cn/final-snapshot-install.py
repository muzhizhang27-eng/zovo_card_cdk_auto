#!/usr/bin/env python3
"""Final CDK copy only. Default: read-only gates. --execute never stops/starts units.

Local Python >=3.7; SG backup Python >=3.7; Ali installation Python >=3.6.
All private artifacts stay outside the repository in fresh 0700 directories.
"""
import argparse
from contextlib import contextmanager
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import pwd
import re
import shlex
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
from urllib.parse import urlsplit

ROLES = ('recharge', 'admin')
UNITS = tuple('cdk-' + role + '.service' for role in ('recharge', 'admin', 'lookup'))
PROXY_KEYS = ('CARD_API_PROXY_URL', 'GPTCHECK_PROXY_URL')
FILES = {'recharge.db': '/var/lib/cdk-recharge/cdk_recharge.db',
         'admin.db': '/var/lib/cdk-admin/cdk_recharge.db',
         'recharge.env': '/etc/cdk-recharge/app.env',
         'admin.env': '/etc/cdk-admin/app.env',
         'manifest.private.json': '/var/lib/cdk-admin/manifest.private.json'}
SSH = ['ssh', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=12',
       '-o', 'ServerAliveInterval=10', '-o', 'ServerAliveCountMax=3']
SNAPSHOT = None


class Refused(Exception):
    pass


def digest(data):
    return hashlib.sha256(data).hexdigest()


def metadata(path):
    p = Path(path)
    s = p.lstat()
    if not stat.S_ISREG(s.st_mode):
        raise Refused('regular file required')
    return {'bytes': s.st_size, 'sha256': digest(p.read_bytes()),
            'uid': s.st_uid, 'gid': s.st_gid, 'mode': stat.S_IMODE(s.st_mode)}


def write_new(path, data, mode=0o600, uid=None, gid=None):
    fd = os.open(str(path), os.O_WRONLY | os.O_CREAT | os.O_EXCL, mode)
    with os.fdopen(fd, 'wb') as handle:
        handle.write(data)
        handle.flush()
        os.fsync(handle.fileno())
        os.fchmod(handle.fileno(), mode)
        if uid is not None:
            os.fchown(handle.fileno(), uid, gid)


def sync_dir(path):
    fd = os.open(str(path), os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


@contextmanager
def exclusive_lock(path):
    fd = os.open(str(path), os.O_RDWR | os.O_CREAT | getattr(os, 'O_NOFOLLOW', 0), 0o600)
    try:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Refused('another final installation is already running')
        yield
    finally:
        os.close(fd)


def unit_properties(unit):
    keys = ('LoadState', 'ActiveState', 'SubState', 'MainPID', 'ControlPID', 'Result',
            'InactiveEnterTimestampMonotonic', 'ExecMainStartTimestampMonotonic', 'NRestarts')
    result = subprocess.run(['systemctl', 'show', unit] + ['--property=' + key for key in keys],
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if result.returncode:
        raise Refused('systemd query failed')
    return dict(line.split('=', 1) for line in result.stdout.decode().splitlines() if '=' in line)


def gates(maintenance, previous=None):
    if os.geteuid() != 0:
        raise Refused('remote root required')
    marker = Path(maintenance)
    if not marker.is_file() or marker.is_symlink():
        raise Refused('maintenance marker missing')
    units = {name: unit_properties(name) for name in UNITS}
    for props in units.values():
        required = {'LoadState': 'loaded', 'ActiveState': 'inactive', 'SubState': 'dead',
                    'MainPID': '0', 'ControlPID': '0', 'Result': 'success'}
        if any(props.get(key) != value for key, value in required.items()):
            raise Refused('all three units must be cleanly inactive')
    watched = tuple(FILES.values())
    for process in Path('/proc').iterdir():
        if not process.name.isdigit():
            continue
        try:
            for fd in (process / 'fd').iterdir():
                try:
                    target = os.readlink(str(fd)).removesuffix(' (deleted)') if hasattr(str, 'removesuffix') else os.readlink(str(fd)).replace(' (deleted)', '')
                    if any(target == path or target in (path + '-wal', path + '-journal') for path in watched):
                        raise Refused('a process still has a protected file open')
                except FileNotFoundError:
                    pass
        except FileNotFoundError:
            pass
    state = {'units': units, 'maintenance': str(marker),
             'maintenance_inode': marker.stat().st_ino, 'maintenance_device': marker.stat().st_dev}
    if previous is not None and state != previous:
        raise Refused('freeze state changed during copy')
    return state


def checked_snapshot(source, output):
    before = SNAPSHOT.fingerprint(source)
    original = metadata(source)
    report = SNAPSHOT.backup(source, output)
    after = SNAPSHOT.fingerprint(source)
    if original != metadata(source) or not SNAPSHOT.comparison(after, before)['match'] or not SNAPSHOT.comparison(report, before)['match']:
        raise Refused('source changed or snapshot differs')
    return report


def source_export(maintenance, expected_freeze=None):
    freeze = gates(maintenance, expected_freeze)
    directory = Path(tempfile.mkdtemp(prefix='cdk-final-source-', dir='/var/tmp'))
    os.chmod(str(directory), 0o700)
    original = {name: metadata(path) for name, path in FILES.items()}
    report = {'databases': {}, 'files': {}, 'source_original': original, 'freeze': freeze}
    for role in ROLES:
        report['databases'][role] = checked_snapshot(FILES[role + '.db'], directory / (role + '.db'))
    for name in ('recharge.env', 'admin.env', 'manifest.private.json'):
        write_new(directory / name, Path(FILES[name]).read_bytes())
    for name in FILES:
        report['files'][name] = metadata(directory / name)
    if original != {name: metadata(path) for name, path in FILES.items()}:
        raise Refused('source private files changed')
    gates(maintenance, freeze)
    write_new(directory / 'report.json', json.dumps(report, sort_keys=True).encode())
    archive = directory / 'bundle.tar.gz'
    with tarfile.open(str(archive), 'w:gz') as bundle:
        for name in tuple(FILES) + ('report.json',):
            bundle.add(str(directory / name), arcname=name, recursive=False)
    os.chmod(str(archive), 0o600)
    return {'directory': str(directory), 'archive': str(archive),
            'archive_metadata': metadata(archive), 'report': report}


def safe_extract(archive, directory):
    expected = set(FILES) | {'report.json'}
    with tarfile.open(str(archive), 'r:gz') as bundle:
        entries = bundle.getmembers()
        if len(entries) != len(expected) or {entry.name for entry in entries} != expected:
            raise Refused('unexpected archive members')
        for entry in entries:
            if not entry.isfile() or entry.size > 64 * 1024 * 1024:
                raise Refused('unsafe archive member')
            with bundle.extractfile(entry) as source:
                write_new(Path(directory) / entry.name, source.read())
    report = json.loads((Path(directory) / 'report.json').read_text())
    for name in FILES:
        actual = metadata(Path(directory) / name)
        if actual['sha256'] != report['files'][name]['sha256'] or actual['bytes'] != report['files'][name]['bytes']:
            raise Refused('archive file hash mismatch')
    for role in ROLES:
        actual = SNAPSHOT.fingerprint(Path(directory) / (role + '.db'))
        if not SNAPSHOT.comparison(actual, report['databases'][role])['match']:
            raise Refused('archive database fingerprint mismatch')
    return report


def proxy_lines(content):
    result = {}
    for line in content.splitlines(keepends=True):
        match = re.match(br'^\s*([A-Za-z_][A-Za-z_0-9]*)\s*=(.*?)[\r\n]*$', line)
        if match and match.group(1).decode() in PROXY_KEYS:
            key = match.group(1).decode()
            if key in result:
                raise Refused('duplicate proxy key')
            tokens = shlex.split(match.group(2).decode(), comments=False)
            if len(tokens) != 1:
                raise Refused('invalid proxy assignment')
            result[key] = (tokens[0], line)
    return result


def merge_env(source, target, expected):
    old, new = proxy_lines(target), proxy_lines(source)
    for value in expected.values():
        u = urlsplit(value)
        if u.scheme != 'http' or u.hostname != '127.0.0.1' or not u.port or u.username or u.password or u.path not in ('', '/') or u.query or u.fragment:
            raise Refused('unit proxy must use explicit private loopback port')
    for key, (value, _) in dict(old, **new).items():
        if expected.get(key) != value:
            raise Refused('proxy assignment conflicts with destination unit')
    # Check both maps separately; a source value may not mask a conflicting target.
    if any(expected.get(key) != item[0] for key, item in old.items()):
        raise Refused('destination proxy assignment conflicts with unit')
    result = source
    for key in PROXY_KEYS:
        if key in old and key not in new:
            if result and not result.endswith(b'\n'):
                result += b'\n'
            result += old[key][1].rstrip(b'\r\n') + b'\n'
    return result


def parse_execstart_proxies(rendered, role):
    # Read effective ExecStart from systemd, not a possibly overridden unit file.
    # These reviewed units deliberately use env(1) so EnvironmentFile cannot
    # override the proxy route. Only their exact one-executable form is accepted.
    match = re.fullmatch(r'\{\s*path=/usr/bin/env\s*;\s*argv\[\]=(.*?)\s*;\s*ignore_errors=no\s*;.*\}', rendered.strip(), re.S)
    if not match:
        raise Refused('expected single explicit env ExecStart required')
    argv = shlex.split(match.group(1))
    if not argv or argv[0] != '/usr/bin/env':
        raise Refused('unexpected ExecStart command')
    values, index = {}, 1
    while index < len(argv) and re.match(r'^[A-Za-z_][A-Za-z_0-9]*=', argv[index]):
        key, value = argv[index].split('=', 1)
        if key in values or key not in PROXY_KEYS + ('GOMAXPROCS', 'GOMEMLIMIT'):
            raise Refused('unexpected or duplicate env ExecStart assignment')
        values[key] = value
        index += 1
    if argv[index:] != ['/opt/cdk-' + role + '/current/cdk-' + role]:
        raise Refused('unexpected application executable or extra arguments')
    result = {key: values[key] for key in PROXY_KEYS if key in values}
    required = {'CARD_API_PROXY_URL'} if role == 'lookup' else set(PROXY_KEYS)
    if set(result) != required:
        raise Refused('required application proxy variables missing from ExecStart')
    merge_env(b'', b'', result)  # Validate loopback-only URL syntax, without values in output.
    return result


def unit_proxies(role):
    result = subprocess.run(['systemctl', 'show', 'cdk-' + role + '.service', '--property=ExecStart', '--value'],
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if result.returncode:
        raise Refused('unit ExecStart query failed')
    return parse_execstart_proxies(result.stdout.decode(), role)


def install_files(staging, report, target_paths, expected_proxies, guard, backup_dir, enforce_owners=True):
    """Pure filesystem transaction with injectable test gate; not a service control."""
    staging, backup_dir = Path(staging), Path(backup_dir)
    backup_dir.mkdir(mode=0o700)
    originals, incoming, staged, env_merges = {}, {}, {}, {}
    guard()
    for name, destination in target_paths.items():
        originals[name] = metadata(destination)
        if name.endswith('.db'):
            if any(Path(destination + suffix).exists() for suffix in ('-wal', '-shm', '-journal')):
                raise Refused('destination database has sidecars; manual review required')
            SNAPSHOT.fingerprint(destination)
        if enforce_owners:
            role = 'recharge' if name.startswith('recharge.') else 'admin'
            account = pwd.getpwnam('cdk-' + role)
            expected_uid = 0 if name.endswith('.env') else account.pw_uid
            expected_mode = 0o640 if name.endswith('.env') else 0o600
            current = originals[name]
            if current['uid'] != expected_uid or current['gid'] != account.pw_gid or current['mode'] != expected_mode:
                raise Refused('destination ownership or permissions differ')
        write_new(backup_dir / name, Path(destination).read_bytes())
        data = (staging / name).read_bytes()
        if name.endswith('.env'):
            role = name.split('.')[0]
            data = merge_env(data, Path(destination).read_bytes(), expected_proxies[role])
            env_merges[role] = {'source_sha256': digest((staging / name).read_bytes()),
                                'previous_target_sha256': originals[name]['sha256'],
                                'installed_sha256': digest(data),
                                'proxy_keys_in_result': sorted(proxy_lines(data))}
        incoming[name] = digest(data)
        fd, filename = tempfile.mkstemp(prefix='.cdk-final-', dir=str(Path(destination).parent))
        os.close(fd)
        os.unlink(filename)
        write_new(filename, data, originals[name]['mode'], originals[name]['uid'], originals[name]['gid'])
        staged[name] = filename
    write_new(backup_dir / 'original-metadata.json', json.dumps(originals, sort_keys=True).encode())
    sync_dir(backup_dir)
    guard()
    if any(metadata(path) != originals[name] for name, path in target_paths.items()):
        raise Refused('destination changed after rehearsal backup')
    replaced = []
    try:
        for name, destination in target_paths.items():
            guard()
            if metadata(destination) != originals[name]:
                raise Refused('destination changed immediately before replacement')
            os.replace(staged[name], destination)
            replaced.append(name)
            sync_dir(Path(destination).parent)
        out = {'files': {}, 'databases': {}, 'env_merges': env_merges,
               'backup_directory': str(backup_dir)}
        for name, destination in target_paths.items():
            actual = metadata(destination)
            if actual['sha256'] != incoming[name] or any(actual[key] != originals[name][key] for key in ('uid', 'gid', 'mode')):
                raise Refused('installed bytes or ownership differ')
            out['files'][name] = actual
        for role in ROLES:
            actual = SNAPSHOT.fingerprint(target_paths[role + '.db'])
            check = SNAPSHOT.comparison(actual, report['databases'][role])
            if not check['match']:
                raise Refused('installed database differs')
            out['databases'][role] = {'comparison': check, 'fingerprint': actual}
        guard()
        out['status'] = 'installed-services-still-stopped'
        return out
    except Exception:
        # Services must remain stopped. Never restore an old DB into a running app.
        guard()
        for name in reversed(replaced):
            destination = target_paths[name]
            fd, restore = tempfile.mkstemp(prefix='.cdk-restore-', dir=str(Path(destination).parent))
            os.close(fd)
            os.unlink(restore)
            old = originals[name]
            write_new(restore, (backup_dir / name).read_bytes(), old['mode'], old['uid'], old['gid'])
            os.replace(restore, destination)
            sync_dir(Path(destination).parent)
            if metadata(destination) != old:
                raise Refused('rollback validation failed; keep maintenance and inspect')
        raise
    finally:
        for filename in staged.values():
            if Path(filename).exists():
                Path(filename).unlink()


def target_install(directory, maintenance, archive_sha, source_report, expected_freeze=None):
    freeze = gates(maintenance, expected_freeze)
    directory = Path(directory)
    archive = directory / 'bundle.tar.gz'
    if metadata(archive)['sha256'] != archive_sha:
        raise Refused('uploaded archive differs')
    unpack = directory / 'unpacked'
    unpack.mkdir(mode=0o700)
    report = safe_extract(archive, unpack)
    if report != source_report:
        raise Refused('source manifest differs')
    expected = {role: unit_proxies(role) for role in ROLES}
    lookup = unit_proxies('lookup')
    if any(value['CARD_API_PROXY_URL'] != lookup['CARD_API_PROXY_URL'] for value in expected.values()):
        raise Refused('three unit card proxy routes differ')
    return install_files(unpack, report, FILES, expected,
                         lambda: gates(maintenance, freeze), directory / 'rehearsal-backup')


def remote_action(action, params):
    os.umask(0o077)
    if action == 'gates':
        return gates(params['maintenance'], params.get('freeze'))
    if action == 'source-export':
        return source_export(params['maintenance'], params['freeze'])
    if action == 'source-recheck':
        gates(params['maintenance'], params['report']['freeze'])
        if {name: metadata(path) for name, path in FILES.items()} != params['report']['source_original']:
            raise Refused('source changed after export')
        return {'source_still_frozen_and_unchanged': True}
    if action == 'target-stage':
        gates(params['maintenance'], params['freeze'])
        directory = tempfile.mkdtemp(prefix='cdk-final-target-', dir='/root')
        os.chmod(directory, 0o700)
        return {'directory': directory}
    if action == 'target-install':
        with exclusive_lock('/run/lock/cdk-final-copy.lock'):
            return target_install(params['directory'], params['maintenance'], params['archive_sha'], params['source_report'], params['freeze'])
    raise Refused('unknown action')


def ssh_base(host):
    return SSH + (['-J', 'aliyun-prod'] if host == 'sg-prod' else []) + [host]


def remote(host, action, params):
    directory = Path(__file__).resolve().parent
    code = 'import types,json,sys\n'
    code += 's=types.ModuleType("snapshot")\nexec(compile(' + repr((directory / 'sqlite-snapshot.py').read_text()) + ',"snapshot","exec"),s.__dict__)\n'
    code += 'm={"__name__":"final_copy","SNAPSHOT":s}\nexec(compile(' + repr(Path(__file__).read_text()) + ',"final-copy","exec"),m)\nm["SNAPSHOT"]=s\n'
    code += 'try:\n r=m["remote_action"](' + repr(action) + ',' + repr(params) + ')\n print(json.dumps({"ok":True,"result":r}))\n'
    code += 'except Exception as e:\n print(json.dumps({"ok":False,"error_type":type(e).__name__,"reason":str(e) if isinstance(e,m["Refused"]) else "raw details suppressed","action":' + repr(action) + '}))\n sys.exit(2)\n'
    command = 'sudo -n python3 -' if host == 'sg-prod' else 'python3 -'
    result = subprocess.run(ssh_base(host) + [command], input=code.encode(), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    try:
        report = json.loads(result.stdout)
    except (ValueError, UnicodeDecodeError):
        raise Refused('SSH response invalid; raw output suppressed')
    if result.returncode or not report.get('ok'):
        raise Refused('remote ' + action + ' refused: ' + report.get('reason', 'raw output suppressed'))
    return report['result']


def main():
    global SNAPSHOT
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--execute', action='store_true')
    parser.add_argument('--maintenance-file', required=True, help='Exact sentinel installed on both hosts by the cutover operator')
    parser.add_argument('--work-dir', help='Fresh absolute local directory outside repository; needed only with --execute')
    args = parser.parse_args()
    if args.maintenance_file != '/var/lib/cdk-migration/maintenance':
        raise Refused('expected both-host CDK maintenance sentinel required')
    spec = importlib.util.spec_from_file_location('snapshot', Path(__file__).with_name('sqlite-snapshot.py'))
    SNAPSHOT = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(SNAPSHOT)
    # No remote file writes before both hosts have passed the safety gates.
    checks = {host: remote(host, 'gates', {'maintenance': args.maintenance_file}) for host in ('sg-prod', 'aliyun-prod')}
    if not args.execute:
        print(json.dumps({'status': 'read-only-gates-passed-no-copy', 'gates': checks}, indent=2))
        return
    if not args.work_dir:
        raise Refused('--execute requires a fresh --work-dir')
    work = Path(args.work_dir).resolve()
    repository = Path(__file__).resolve().parents[2]
    temporary_roots = (Path('/tmp').resolve(), Path('/var/tmp').resolve())
    if not Path(args.work_dir).is_absolute() or work == repository or repository in work.parents or work.exists() or not any(root in work.parents for root in temporary_roots):
        raise Refused('fresh private work directory under /tmp or /var/tmp required')
    os.umask(0o077)
    work.mkdir(mode=0o700)
    source = remote('sg-prod', 'source-export', {'maintenance': args.maintenance_file, 'freeze': checks['sg-prod']})
    write_new(work / 'source-report.json', json.dumps(source, sort_keys=True).encode())
    archive = work / 'bundle.tar.gz'
    fd = os.open(str(archive), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'wb') as output:
        result = subprocess.run(ssh_base('sg-prod') + ['sudo -n cat -- ' + shlex.quote(source['archive'])], stdout=output, stderr=subprocess.PIPE)
    if result.returncode or metadata(archive)['sha256'] != source['archive_metadata']['sha256']:
        raise Refused('download hash mismatch')
    unpack = work / 'unpacked'
    unpack.mkdir(mode=0o700)
    if safe_extract(archive, unpack) != source['report']:
        raise Refused('downloaded report differs')
    target = remote('aliyun-prod', 'target-stage', {'maintenance': args.maintenance_file, 'freeze': checks['aliyun-prod']})
    write_new(work / 'target-stage.json', json.dumps(target).encode())
    with archive.open('rb') as incoming:
        command = 'umask 077; set -C; cat > ' + shlex.quote(target['directory'] + '/bundle.tar.gz')
        result = subprocess.run(ssh_base('aliyun-prod') + [command], stdin=incoming, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if result.returncode:
        raise Refused('upload failed')
    remote('sg-prod', 'source-recheck', {'maintenance': args.maintenance_file, 'report': source['report']})
    outcome = remote('aliyun-prod', 'target-install', {'directory': target['directory'], 'maintenance': args.maintenance_file,
                    'archive_sha': source['archive_metadata']['sha256'], 'source_report': source['report'], 'freeze': checks['aliyun-prod']})
    write_new(work / 'target-result.json', json.dumps(outcome, sort_keys=True).encode())
    remote('sg-prod', 'source-recheck', {'maintenance': args.maintenance_file, 'report': source['report']})
    remote('aliyun-prod', 'gates', {'maintenance': args.maintenance_file, 'freeze': checks['aliyun-prod']})
    print(json.dumps({'status': outcome['status'], 'work_directory': str(work), 'target_directory': target['directory'],
                      'databases': {role: {'comparison': outcome['databases'][role]['comparison'],
                                           'tables': outcome['databases'][role]['fingerprint']['table_count'],
                                           'rows': outcome['databases'][role]['fingerprint']['row_count']} for role in ROLES},
                      'private_files': outcome['files']}, indent=2, sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print(json.dumps({'status': 'refused-or-failed-keep-services-stopped', 'error_type': type(error).__name__,
                          'reason': str(error) if isinstance(error, Refused) else 'raw details suppressed'}), file=sys.stderr)
        sys.exit(2)
