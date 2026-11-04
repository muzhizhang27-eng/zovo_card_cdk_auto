import importlib.util
import io
import json
import os
from pathlib import Path
import sqlite3
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('sqlite_snapshot', Path(__file__).with_name('sqlite-snapshot.py'))
snapshot = importlib.util.module_from_spec(spec)
spec.loader.exec_module(snapshot)


class SnapshotTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.root = Path(self.directory.name)

    def tearDown(self):
        self.directory.cleanup()

    def create(self, filename='source.db', values=None):
        path = self.root / filename
        connection = sqlite3.connect(str(path))
        connection.execute('CREATE TABLE records(value)')
        if values is None:
            values = [None, '', sqlite3.Binary(b''), 0, '0', sqlite3.Binary(b'0'),
                      9223372036854775807, -9223372036854775808, 1.5,
                      'SYNTHETIC_PRIVATE_VALUE', sqlite3.Binary(b'\x00\xff\n\t')]
        connection.executemany('INSERT INTO records VALUES (?)', [(value,) for value in values])
        connection.commit()
        connection.close()
        return path

    def test_storage_classes_are_distinct(self):
        examples = [(b'null', None), (b'text', b''), (b'blob', b''), (b'integer', 0),
                    (b'real', 0.0), (b'text', b'0'), (b'blob', b'0')]
        self.assertEqual(len({snapshot.row_digest(row) for row in examples}), len(examples))

    def test_fingerprint_is_readonly_and_contains_no_values(self):
        path = self.create()
        before = path.read_bytes()
        first = snapshot.fingerprint(path)
        self.assertEqual(first['row_count'], 11)
        self.assertEqual(first['overall_sha256'], snapshot.fingerprint(path)['overall_sha256'])
        self.assertNotIn('SYNTHETIC_PRIVATE_VALUE', json.dumps(first))
        self.assertEqual(path.read_bytes(), before)

    def test_row_order_is_irrelevant_but_duplicates_count(self):
        left = snapshot.fingerprint(self.create('a.db', [1, 'x', None, 'x']))
        right = snapshot.fingerprint(self.create('b.db', ['x', None, 'x', 1]))
        changed = snapshot.fingerprint(self.create('c.db', ['x', None, 1]))
        self.assertTrue(snapshot.comparison(right, left)['match'])
        self.assertFalse(snapshot.comparison(changed, left)['match'])

    def test_backup_preserves_trigger_autoincrement_and_data(self):
        path = self.create()
        c = sqlite3.connect(str(path))
        c.executescript('CREATE TABLE seq(id INTEGER PRIMARY KEY AUTOINCREMENT,value);'
                        'CREATE TABLE audit(id INTEGER);'
                        'CREATE TRIGGER audit_insert AFTER INSERT ON seq BEGIN INSERT INTO audit VALUES(new.id); END;'
                        'INSERT INTO seq(value) VALUES(1); DELETE FROM seq;')
        c.close()
        before = snapshot.fingerprint(path)
        destination = self.root / 'backup.db'
        after = snapshot.backup(path, destination)
        self.assertTrue(snapshot.comparison(after, before)['match'])
        self.assertEqual(destination.stat().st_mode & 0o777, 0o600)
        self.assertIn('sqlite_sequence', [table['name'] for table in after['tables']])
        self.assertIn('trigger', [obj['type'] for obj in after['schema_objects']])

    def test_existing_destination_and_same_source_are_refused(self):
        path = self.create()
        with self.assertRaises(snapshot.SnapshotError):
            snapshot.backup(path, path)
        destination = self.root / 'existing.db'
        destination.write_bytes(b'do not overwrite')
        with self.assertRaises(FileExistsError):
            snapshot.backup(path, destination)
        self.assertEqual(destination.read_bytes(), b'do not overwrite')

    def test_row_bound_rejects_partial_report(self):
        with self.assertRaises(snapshot.SnapshotError):
            snapshot.fingerprint(self.create(), max_rows=2)

    def test_online_backup_includes_uncheckpointed_wal(self):
        path = self.create()
        writer = sqlite3.connect(str(path))
        try:
            writer.execute('PRAGMA journal_mode=WAL')
            writer.execute('PRAGMA wal_autocheckpoint=0')
            writer.execute("INSERT INTO records VALUES('uncheckpointed synthetic value')")
            writer.commit()
            self.assertTrue(Path(str(path) + '-wal').exists())
            source = snapshot.fingerprint(path)
            dest = snapshot.backup(path, self.root / 'wal-backup.db')
            self.assertTrue(snapshot.comparison(dest, source)['match'])
            self.assertEqual(dest['row_count'], 12)
            self.assertEqual(dest['journal_mode'], 'delete')
            self.assertEqual(writer.execute('PRAGMA journal_mode').fetchone()[0], 'wal')
            self.assertFalse(Path(str(self.root / 'wal-backup.db') + '-wal').exists())
            self.assertFalse(Path(str(self.root / 'wal-backup.db') + '-shm').exists())
        finally:
            writer.close()

    def test_schema_change_is_detected(self):
        path = self.create()
        before = snapshot.fingerprint(path)
        c = sqlite3.connect(str(path));c.execute('CREATE INDEX extra ON records(value)');c.close()
        after = snapshot.fingerprint(path)
        self.assertFalse(snapshot.comparison(after, before)['match'])


if __name__ == '__main__':
    unittest.main()
