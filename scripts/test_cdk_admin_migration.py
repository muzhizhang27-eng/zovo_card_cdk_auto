"""Synthetic, isolated SQLite tests; no production DB or network access."""

import importlib.util
import json
from pathlib import Path
import sqlite3
import stat
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("cdk-admin-migration.py")
SPEC = importlib.util.spec_from_file_location("cdk_admin_migration", SCRIPT)
MIGRATION = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MIGRATION)


class MigrationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="cdk-migration-synthetic-")
        self.root = Path(self.temporary.name)
        self.source = self.root / "source.sqlite3"
        self.writer = sqlite3.connect(self.source)
        self.writer.execute("PRAGMA journal_mode=WAL")
        self.writer.executescript("""
            CREATE TABLE admin_users(id INTEGER PRIMARY KEY,username TEXT,password_hash TEXT,is_active INTEGER DEFAULT 1);
            CREATE TABLE cardplatform_cdk_codes(upstream_id INTEGER,code TEXT UNIQUE,plan TEXT,status TEXT);
            CREATE TABLE site_settings(key TEXT PRIMARY KEY,value TEXT);
            CREATE TABLE admin_audit_logs(id INTEGER PRIMARY KEY AUTOINCREMENT,action TEXT);
            CREATE TABLE cdk_session_bindings(cdk_code TEXT PRIMARY KEY,session_payload TEXT,redemption_token TEXT);
            CREATE TABLE card_product_cache(product_code TEXT PRIMARY KEY,synced_at TEXT);
            CREATE TABLE typed_values(i INTEGER,r REAL,t TEXT,b BLOB,n TEXT);
        """)
        self.writer.execute("INSERT INTO admin_users(id,username,password_hash) VALUES(1,'admin',?)", ("synthetic-hash-do-not-print",))
        self.writer.executemany("INSERT INTO cardplatform_cdk_codes VALUES(?,?,?,?)",
                                [(i, "synthetic-code-" + str(i), "plus", "unused") for i in range(14)])
        self.writer.execute("INSERT INTO site_settings VALUES('card_api_key',?)", ("synthetic-key-do-not-print",))
        self.writer.execute("INSERT INTO admin_audit_logs(action) VALUES('historical')")
        self.writer.execute("INSERT INTO cdk_session_bindings VALUES(?,?,?)",
                            ("synthetic-code-0", "synthetic-session-do-not-print", "synthetic-token-do-not-print"))
        self.writer.execute("INSERT INTO card_product_cache VALUES('sample','before')")
        self.writer.execute("INSERT INTO typed_values VALUES(1,1.25,'text',?,NULL)", (b"\x00\xff",))
        self.writer.commit()

    def tearDown(self):
        self.writer.close()
        self.temporary.cleanup()

    def snapshot(self):
        directory = self.root / "snapshot"
        result = MIGRATION.make_snapshot(self.source, directory, 14, 1)
        self.assertTrue(result["ok"])
        return directory, MIGRATION.load_manifest(directory / MIGRATION.MANIFEST_NAME)

    def test_online_backup_reads_committed_wal_without_uncommitted_password_change(self):
        self.writer.execute("UPDATE admin_users SET password_hash='uncommitted-change'")
        directory, manifest = self.snapshot()
        with sqlite3.connect(directory / MIGRATION.SNAPSHOT_NAME) as connection:
            self.assertEqual(connection.execute("SELECT password_hash FROM admin_users").fetchone()[0],
                             "synthetic-hash-do-not-print")
            self.assertEqual(connection.execute("SELECT COUNT(*) FROM cardplatform_cdk_codes").fetchone()[0], 14)
        self.assertEqual(manifest["tables"]["admin_users"]["count"], 1)
        for path, mode in ((directory, 0o700),
                           (directory / MIGRATION.SNAPSHOT_NAME, 0o600),
                           (directory / MIGRATION.MANIFEST_NAME, 0o600)):
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), mode)

    def test_install_new_preserves_data_and_refuses_overwrites_and_public_tree(self):
        directory, manifest = self.snapshot()
        public_dir = self.root / "public"
        public_dir.mkdir()
        public = public_dir / "cdk_recharge.db"
        public.write_bytes(b"public-sentinel")
        target = self.root / "admin"
        result = MIGRATION.install_new(directory / MIGRATION.SNAPSHOT_NAME,
                                       directory / MIGRATION.MANIFEST_NAME, target, public)
        self.assertTrue(result["ok"])
        self.assertTrue(MIGRATION.verify_database(target / MIGRATION.DATABASE_NAME, manifest)["ok"])
        for rejected in (target, public_dir, public_dir / "nested"):
            with self.assertRaises(MIGRATION.MigrationFailure):
                MIGRATION.install_new(directory / MIGRATION.SNAPSHOT_NAME,
                                      directory / MIGRATION.MANIFEST_NAME, rejected, public)
        self.assertEqual(public.read_bytes(), b"public-sentinel")

    def test_running_phase_allows_cache_updates_and_audit_append_but_not_history_edits(self):
        directory, manifest = self.snapshot()
        database = directory / MIGRATION.SNAPSHOT_NAME
        with sqlite3.connect(database) as connection:
            connection.execute("UPDATE card_product_cache SET synced_at='after'")
            connection.execute("INSERT INTO admin_audit_logs(action) VALUES('login')")
        self.assertFalse(MIGRATION.verify_database(database, manifest)["ok"])
        self.assertTrue(MIGRATION.verify_database(database, manifest, "running")["ok"])
        with sqlite3.connect(database) as connection:
            connection.execute("UPDATE admin_audit_logs SET action='altered' WHERE id=1")
        self.assertFalse(MIGRATION.verify_database(database, manifest, "running")["ok"])

    def test_password_change_fails_verification(self):
        directory, manifest = self.snapshot()
        database = directory / MIGRATION.SNAPSHOT_NAME
        with sqlite3.connect(database) as connection:
            connection.execute("UPDATE admin_users SET password_hash='different'")
        result = MIGRATION.verify_database(database, manifest, "running")
        self.assertFalse(result["ok"])
        self.assertFalse(next(t for t in result["tables"] if t["table"] == "admin_users")["matches"])

    def test_running_allows_only_exact_additive_session_schema(self):
        directory, manifest = self.snapshot()
        database = directory / MIGRATION.SNAPSHOT_NAME
        with sqlite3.connect(database) as connection:
            connection.executescript(MIGRATION.SESSION_MIGRATION_SQL)
            connection.execute("INSERT INTO admin_sessions VALUES('synthetic-session-hash',1,'synthetic-csrf-hash',2000000000,1900000000)")
        self.assertFalse(MIGRATION.verify_database(database, manifest)["ok"])
        result = MIGRATION.verify_database(database, manifest, "running")
        self.assertTrue(result["ok"])
        self.assertFalse(result["schema_matches"])
        self.assertTrue(result["original_schema_retained"])
        with sqlite3.connect(database) as connection:
            connection.execute("DROP TRIGGER revoke_admin_sessions_on_delete")
        self.assertFalse(MIGRATION.verify_database(database, manifest, "running")["ok"])
        with sqlite3.connect(database) as connection:
            connection.executescript(MIGRATION.SESSION_MIGRATION_SQL)
        with sqlite3.connect(database) as connection:
            connection.execute("CREATE TABLE unexpected_table(value TEXT)")
        self.assertFalse(MIGRATION.verify_database(database, manifest, "running")["ok"])

    def test_running_rejects_changed_original_schema_and_wrong_session_trigger(self):
        directory, manifest = self.snapshot()
        database = directory / MIGRATION.SNAPSHOT_NAME
        with sqlite3.connect(database) as connection:
            connection.executescript(MIGRATION.SESSION_MIGRATION_SQL)
            connection.execute("DROP TRIGGER revoke_admin_sessions_on_delete")
            connection.execute("CREATE TRIGGER revoke_admin_sessions_on_delete AFTER DELETE ON admin_users BEGIN DELETE FROM site_settings; END")
        self.assertFalse(MIGRATION.verify_database(database, manifest, "running")["ok"])
        with sqlite3.connect(database) as connection:
            connection.execute("DROP TRIGGER revoke_admin_sessions_on_delete")
            connection.executescript(MIGRATION.SESSION_MIGRATION_SQL)
            connection.execute("ALTER TABLE site_settings ADD COLUMN extra TEXT")
        self.assertFalse(MIGRATION.verify_database(database, manifest, "running")["ok"])

    def test_install_rejects_broken_symlink_target(self):
        directory, _manifest = self.snapshot()
        target = self.root / "broken-link"
        target.symlink_to(self.root / "unintended-location")
        with self.assertRaises(MIGRATION.MigrationFailure):
            MIGRATION.install_new(directory / MIGRATION.SNAPSHOT_NAME,
                                  directory / MIGRATION.MANIFEST_NAME, target,
                                  self.root / "public" / "cdk_recharge.db")
        self.assertFalse((self.root / "unintended-location").exists())

    def test_cli_output_has_no_values_or_digests(self):
        command = [sys.executable, str(SCRIPT), "snapshot", "--source", str(self.source),
                   "--output-dir", str(self.root / "cli"), "--expected-cdks", "14", "--expected-admins", "1"]
        result = subprocess.run(command, capture_output=True, text=True, check=True)
        self.assertTrue(json.loads(result.stdout)["ok"])
        self.assertEqual(result.stderr, "")
        for secret in ("synthetic-code", "synthetic-key", "synthetic-hash",
                       "synthetic-session", "synthetic-token"):
            self.assertNotIn(secret, result.stdout)
        manifest = MIGRATION.load_manifest(self.root / "cli" / MIGRATION.MANIFEST_NAME)
        self.assertNotIn(manifest["snapshot_sha256"], result.stdout)
        second = subprocess.run(command, capture_output=True, text=True)
        self.assertEqual(second.returncode, 1)
        self.assertTrue(json.loads(second.stdout)["target_already_exists"])


if __name__ == "__main__":
    unittest.main()
