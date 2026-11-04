import copy
import importlib.util
import json
from pathlib import Path
import sqlite3
import tarfile
import tempfile
import unittest


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


tool = module('final_copy', 'final-snapshot-install.py')
tool.SNAPSHOT = module('sqlite_snapshot', 'sqlite-snapshot.py')
PROXIES = {'CARD_API_PROXY_URL': 'http://127.0.0.1:18784',
           'GPTCHECK_PROXY_URL': 'http://127.0.0.1:18785'}


class FinalCopyTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.staging, self.target = self.root / 'incoming', self.root / 'target'
        self.staging.mkdir(); self.target.mkdir()
        self.paths = {}
        self.report = {'databases': {}, 'files': {}}
        for role in tool.ROLES:
            for parent, value in ((self.staging, 'latest-synthetic-data'), (self.target, 'old-synthetic-data')):
                db = parent / (role + '.db')
                c = sqlite3.connect(str(db))
                c.execute('CREATE TABLE records(id INTEGER PRIMARY KEY,value BLOB)')
                c.execute('INSERT INTO records VALUES(1,?)', (value.encode(),))
                c.commit(); c.close(); db.chmod(0o600)
            self.report['databases'][role] = tool.SNAPSHOT.fingerprint(self.staging / (role + '.db'))
        for name in ('recharge.env', 'admin.env', 'manifest.private.json'):
            (self.staging / name).write_bytes(b'JWT_SECRET=NEW_SYNTHETIC_PRIVATE\n' if name.endswith('.env') else b'{"synthetic":"new"}')
            (self.target / name).write_bytes(b'JWT_SECRET=OLD_SYNTHETIC_PRIVATE\nCARD_API_PROXY_URL=http://127.0.0.1:18784\n' if name.endswith('.env') else b'{"synthetic":"old"}')
        for name in tool.FILES:
            self.paths[name] = str(self.target / name)
            self.report['files'][name] = tool.metadata(self.staging / name)
        self.originals = {name: Path(path).read_bytes() for name, path in self.paths.items()}
        self.expected = {role: PROXIES for role in tool.ROLES}

    def tearDown(self):
        self.temp.cleanup()

    def install(self, report=None, guard=lambda: None):
        return tool.install_files(self.staging, report or self.report, self.paths, self.expected,
                                  guard, self.root / 'backup', enforce_owners=False)

    def test_complete_copy_preserves_target_proxy_and_never_reports_secret(self):
        outcome = self.install()
        self.assertEqual(outcome['status'], 'installed-services-still-stopped')
        for role in tool.ROLES:
            content = (self.target / (role + '.env')).read_bytes()
            self.assertIn(b'JWT_SECRET=NEW_SYNTHETIC_PRIVATE', content)
            self.assertIn(b'CARD_API_PROXY_URL=http://127.0.0.1:18784', content)
            self.assertTrue(outcome['databases'][role]['comparison']['match'])
        self.assertNotIn('SYNTHETIC_PRIVATE', json.dumps(outcome))
        for name in tool.FILES:
            self.assertEqual((self.root / 'backup' / name).read_bytes(), self.originals[name])

    def test_inactive_gate_refusal_changes_no_destination(self):
        def stopped():
            raise tool.Refused('synthetic active service')
        with self.assertRaises(tool.Refused):
            self.install(guard=stopped)
        self.assertEqual({name: Path(path).read_bytes() for name, path in self.paths.items()}, self.originals)

    def test_postinstall_mismatch_restores_whole_old_set(self):
        invalid = copy.deepcopy(self.report)
        invalid['databases']['admin']['overall_sha256'] = 'invalid'
        with self.assertRaises(tool.Refused):
            self.install(invalid)
        self.assertEqual({name: Path(path).read_bytes() for name, path in self.paths.items()}, self.originals)

    def test_existing_wal_sidecar_refuses_replacement(self):
        Path(self.paths['admin.db'] + '-wal').write_bytes(b'synthetic')
        with self.assertRaises(tool.Refused):
            self.install()
        self.assertEqual({name: Path(path).read_bytes() for name, path in self.paths.items()}, self.originals)

    def test_proxy_conflict_or_duplicate_fails_closed(self):
        for source, target in ((b'CARD_API_PROXY_URL=http://127.0.0.1:1\n', b''),
                               (b'', b'CARD_API_PROXY_URL=http://example.com:8080\n'),
                               (b'', b'CARD_API_PROXY_URL=http://127.0.0.1:18784\nCARD_API_PROXY_URL=http://127.0.0.1:18784\n')):
            with self.assertRaises(tool.Refused):
                tool.merge_env(source, target, PROXIES)

    def test_proxies_in_units_leave_source_bytes_exact(self):
        original = b'# private values\nJWT_SECRET=SYNTHETIC\n'
        self.assertEqual(tool.merge_env(original, original, PROXIES), original)

    def test_archive_rejects_unexpected_paths_before_extraction(self):
        archive = self.root / 'bad.tar.gz'
        with tarfile.open(str(archive), 'w:gz') as tar:
            tar.add(str(self.staging / 'admin.env'), arcname='../escaped.env')
        out = self.root / 'out'; out.mkdir()
        with self.assertRaises(tool.Refused):
            tool.safe_extract(archive, out)
        self.assertEqual(list(out.iterdir()), [])

    def test_simultaneous_installer_is_refused(self):
        with tool.exclusive_lock(self.root / 'lock'):
            with self.assertRaises(tool.Refused):
                with tool.exclusive_lock(self.root / 'lock'):
                    self.fail('second installation acquired lock')

    def execstart(self, role, extra=''):
        assignments = 'GOMAXPROCS=1 GOMEMLIMIT=144MiB CARD_API_PROXY_URL=http://127.0.0.1:18784 '
        if role != 'lookup':
            assignments += 'GPTCHECK_PROXY_URL=http://127.0.0.1:18785 '
        return '{ path=/usr/bin/env ; argv[]=/usr/bin/env ' + assignments + extra + '/opt/cdk-' + role + '/current/cdk-' + role + ' ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }'

    def test_effective_env_execstart_proxy_arguments_are_verified(self):
        self.assertEqual(tool.parse_execstart_proxies(self.execstart('admin'), 'admin'), PROXIES)
        self.assertEqual(tool.parse_execstart_proxies(self.execstart('lookup'), 'lookup'),
                         {'CARD_API_PROXY_URL': PROXIES['CARD_API_PROXY_URL']})

    def test_execstart_duplicate_missing_proxy_or_extra_command_is_rejected(self):
        cases = [self.execstart('admin', 'CARD_API_PROXY_URL=http://127.0.0.1:18784 '),
                 self.execstart('admin').replace('GPTCHECK_PROXY_URL=http://127.0.0.1:18785 ', ''),
                 self.execstart('admin').replace('/opt/cdk-admin/current/cdk-admin', '/bin/sh -c unsafe'),
                 self.execstart('admin').replace('http://127.0.0.1:18784', 'http://remote.example:18784')]
        for rendered in cases:
            with self.assertRaises(tool.Refused):
                tool.parse_execstart_proxies(rendered, 'admin')


if __name__ == '__main__':
    unittest.main()
