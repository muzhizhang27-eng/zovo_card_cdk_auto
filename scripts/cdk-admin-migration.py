#!/usr/bin/env python3
"""Snapshot and verify an administrator DB without displaying its contents.

Examples (the destination directories must not already exist):
  python3 scripts/cdk-admin-migration.py snapshot --source data/cdk_recharge.db \
    --output-dir tmp/admin-migration-RELEASE --expected-cdks 14 --expected-admins 1
  python3 scripts/cdk-admin-migration.py install-new \
    --snapshot PRIVATE/snapshot.sqlite3 --manifest PRIVATE/manifest.private.json \
    --target-dir /var/lib/cdk-recharge-admin \
    --public-database /var/lib/cdk-recharge/cdk_recharge.db
  python3 scripts/cdk-admin-migration.py verify \
    --database /var/lib/cdk-recharge-admin/cdk_recharge.db \
    --manifest PRIVATE/manifest.private.json

Run exact verification before starting the service. After startup, --phase running
allows product/plan caches and SQLite sequences to change, and audit rows to be
appended. It permits the explicitly defined admin_sessions migration and its
indexes/triggers, while preserving every original schema object. Session rows
are ephemeral; every other business table must still match. Live inventory synchronization can
legitimately change CDK statuses: use the stored-list API with sync=0 during the
initial verification. This utility never changes passwords, resets accounts,
installs a service, opens a network connection, or writes the source database.

Manifests contain private digests and are mode 0600. stdout contains only table
names, counts, and boolean check results. Transfer snapshots/manifests through
SSH, keep them outside the web root, and give the new service user ownership of
its fresh directory after installation. Do not share a DB with the public app.
"""

import argparse
import base64
import collections
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
import stat
import sys
import time


FORMAT_VERSION = 1
VOLATILE_TABLES = {"plan_status_cache", "card_product_cache", "sqlite_sequence", "admin_sessions"}
SNAPSHOT_NAME = "snapshot.sqlite3"
MANIFEST_NAME = "manifest.private.json"
DATABASE_NAME = "cdk_recharge.db"

# Matches backend/internal/db/admin_sessions.go. This is an explicit migration
# allowlist, not permission to add arbitrary schema objects after migration.
SESSION_MIGRATION_SQL = """
CREATE TABLE IF NOT EXISTS admin_sessions (
    session_hash TEXT PRIMARY KEY,
    admin_id INTEGER NOT NULL,
    csrf_hash TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_admin_sessions_user ON admin_sessions(admin_id);
CREATE INDEX IF NOT EXISTS idx_admin_sessions_expiry ON admin_sessions(expires_at);
CREATE TRIGGER IF NOT EXISTS revoke_admin_sessions_on_change
AFTER UPDATE OF password_hash, is_active, username ON admin_users
WHEN OLD.password_hash IS NOT NEW.password_hash OR OLD.is_active IS NOT NEW.is_active OR OLD.username IS NOT NEW.username
BEGIN DELETE FROM admin_sessions WHERE admin_id = OLD.id; END;
CREATE TRIGGER IF NOT EXISTS revoke_admin_sessions_on_delete
AFTER DELETE ON admin_users
BEGIN DELETE FROM admin_sessions WHERE admin_id = OLD.id; END;
"""


class MigrationFailure(Exception):
    def __init__(self, flag):
        self.flag = flag
        super().__init__(flag)


def quote_identifier(value):
    return '"' + value.replace('"', '""') + '"'


def json_bytes(value):
    return json.dumps(value, ensure_ascii=True, separators=(",", ":")).encode("utf-8")


def tagged_value(value):
    if value is None:
        return ["null"]
    if isinstance(value, bytes):
        return ["blob", base64.b64encode(value).decode("ascii")]
    if isinstance(value, int):
        return ["integer", str(value)]
    if isinstance(value, float):
        return ["real", value.hex()]
    return ["text", value]


def file_digest(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def readonly_connection(path):
    path = Path(path).resolve(strict=True)
    if not path.is_file():
        raise MigrationFailure("database_is_not_file")
    connection = sqlite3.connect(path.as_uri() + "?mode=ro", uri=True, timeout=30)
    connection.execute("PRAGMA query_only=ON")
    return connection


def describe_schema(schema):
    result = {}
    for kind, name, table, sql in schema:
        normalized = re.sub(r"\s+", " ", sql).strip() if sql is not None else None
        result[kind + ":" + name] = {
            "type": kind, "name": name, "table": table,
            "sql_sha256": hashlib.sha256(json_bytes(sql)).hexdigest(),
            "normalized_sql_sha256": hashlib.sha256(json_bytes(normalized)).hexdigest(),
        }
    return result


def allowed_session_schema():
    connection = sqlite3.connect(":memory:")
    try:
        connection.execute("CREATE TABLE admin_users(id INTEGER PRIMARY KEY, password_hash TEXT, is_active INTEGER, username TEXT)")
        connection.executescript(SESSION_MIGRATION_SQL)
        objects = describe_schema(connection.execute(
            "SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name"
        ).fetchall())
        del objects["table:admin_users"]
        return objects
    finally:
        connection.close()


def schema_retained_with_session_migration(current, manifest):
    expected_objects = manifest.get("schema_objects")
    if not expected_objects:
        return False
    current_objects = current["schema_objects"]
    for key, expected in expected_objects.items():
        if current_objects.get(key) != expected:
            return False
    permitted = allowed_session_schema()
    additions = set(current_objects) - set(expected_objects)
    if additions and not set(permitted).issubset(current_objects):
        return False  # Do not accept a partially applied session migration.
    for key in additions:
        actual, expected = current_objects[key], permitted.get(key)
        if expected is None:
            return False
        # SQLite preserves insignificant whitespace in CREATE statements.
        for field in ("type", "name", "table", "normalized_sql_sha256"):
            if actual[field] != expected[field]:
                return False
    return True


def fingerprint_database(path):
    connection = readonly_connection(path)
    try:
        connection.execute("BEGIN")
        if connection.execute("PRAGMA integrity_check").fetchall() != [("ok",)]:
            raise MigrationFailure("database_integrity_failed")
        if connection.execute("PRAGMA foreign_key_check").fetchone() is not None:
            raise MigrationFailure("foreign_key_check_failed")
        schema = connection.execute(
            "SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name"
        ).fetchall()
        tables = {}
        for name, in connection.execute(
            "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name"
        ).fetchall():
            quoted = quote_identifier(name)
            columns = connection.execute("PRAGMA table_info(" + quoted + ")").fetchall()
            row_digests = []
            for row in connection.execute("SELECT * FROM " + quoted):
                encoded = json_bytes([tagged_value(value) for value in row])
                row_digests.append(hashlib.sha256(encoded).hexdigest())
            row_digests.sort()
            tables[name] = {
                "count": len(row_digests),
                "columns_sha256": hashlib.sha256(json_bytes(columns)).hexdigest(),
                "rows_sha256": hashlib.sha256(json_bytes(row_digests)).hexdigest(),
                "row_sha256": row_digests,
            }
        return {
            "schema_sha256": hashlib.sha256(json_bytes(schema)).hexdigest(),
            "schema_objects": describe_schema(schema),
            "tables": tables,
        }
    finally:
        connection.close()


def online_backup(source, destination, timeout=120):
    """SQLite backup API includes committed WAL data; never raw-copy an open DB."""
    destination = Path(destination)
    descriptor = os.open(destination, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    os.close(descriptor)
    source_connection = readonly_connection(source)
    target_connection = sqlite3.connect(destination)
    deadline = time.monotonic() + timeout

    def progress(_status, _remaining, _total):
        if time.monotonic() > deadline:
            raise MigrationFailure("backup_timed_out")

    try:
        source_connection.backup(target_connection, pages=128, progress=progress, sleep=0.05)
        # A source in WAL mode can copy that mode into the backup header. Seal the
        # fresh destination as a self-contained file; never require transferred
        # -wal/-shm sidecars or alter the live source's journal mode.
        if target_connection.execute("PRAGMA journal_mode=DELETE").fetchone()[0] != "delete":
            raise MigrationFailure("snapshot_journal_mode_failed")
    finally:
        target_connection.close()
        source_connection.close()
    destination.chmod(0o600)


def create_private_directory(path):
    path = Path(path).absolute()
    if path.exists() or path.is_symlink():
        raise MigrationFailure("target_already_exists")
    path.mkdir(mode=0o700, parents=False, exist_ok=False)
    path.chmod(0o700)
    return path


def write_private_manifest(path, manifest):
    descriptor = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
        json.dump(manifest, stream, ensure_ascii=True, indent=2)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())


def load_manifest(path):
    path = Path(path)
    if stat.S_IMODE(path.stat().st_mode) & 0o077:
        raise MigrationFailure("manifest_permissions_too_open")
    manifest = json.loads(path.read_text(encoding="utf-8"))
    if manifest.get("format_version") != FORMAT_VERSION:
        raise MigrationFailure("manifest_format_unsupported")
    if not isinstance(manifest.get("tables"), dict):
        raise MigrationFailure("manifest_invalid")
    return manifest


def verify_database(database, manifest, phase="exact"):
    current = fingerprint_database(database)
    schema_matches = current["schema_sha256"] == manifest["schema_sha256"]
    schema_accepted = schema_matches or (
        phase == "running" and schema_retained_with_session_migration(current, manifest))
    result = {"ok": bool(schema_accepted), "integrity_ok": True,
              "schema_matches": schema_matches, "original_schema_retained": bool(schema_accepted),
              "tables": []}
    for name in sorted(set(manifest["tables"]) | set(current["tables"])):
        expected = manifest["tables"].get(name)
        actual = current["tables"].get(name)
        matches = bool(expected is not None and actual is not None
                       and actual["columns_sha256"] == expected["columns_sha256"]
                       and actual["rows_sha256"] == expected["rows_sha256"]
                       and actual["count"] == expected["count"])
        retained = matches
        allowed_to_change = phase == "running" and name in VOLATILE_TABLES
        if phase == "running" and name == "admin_audit_logs" and expected and actual:
            missing = (collections.Counter(expected["row_sha256"])
                       - collections.Counter(actual["row_sha256"]))
            retained = (not missing
                        and actual["columns_sha256"] == expected["columns_sha256"])
        new_session_table = (phase == "running" and name == "admin_sessions"
                             and expected is None and actual is not None and schema_accepted)
        accepted = bool(matches or retained or new_session_table
                        or (allowed_to_change and expected and actual))
        result["tables"].append({
            "table": name,
            "expected_count": expected["count"] if expected else 0,
            "count": actual["count"] if actual else 0,
            "matches": matches,
            "historical_rows_retained": bool(retained),
            "allowed_to_change": allowed_to_change,
            "accepted": accepted,
        })
        result["ok"] = bool(result["ok"] and accepted)
    return result


def make_snapshot(source, output_dir, expected_cdks=None, expected_admins=None):
    source = Path(source).resolve(strict=True)
    output = create_private_directory(output_dir)
    snapshot = output / SNAPSHOT_NAME
    online_backup(source, snapshot)
    manifest = fingerprint_database(snapshot)
    for table, expected in (("cardplatform_cdk_codes", expected_cdks),
                            ("admin_users", expected_admins)):
        if expected is not None and manifest["tables"].get(table, {}).get("count") != expected:
            raise MigrationFailure("expected_counts_do_not_match")
    manifest.update({
        "format_version": FORMAT_VERSION,
        "created_at_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "source_database": str(source),
        "snapshot_sha256": file_digest(snapshot),
    })
    write_private_manifest(output / MANIFEST_NAME, manifest)
    return verify_database(snapshot, manifest)


def install_new(snapshot, manifest_path, target_dir, public_database):
    snapshot = Path(snapshot).resolve(strict=True)
    public_database = Path(public_database).resolve()
    if Path(target_dir).is_symlink():
        raise MigrationFailure("target_already_exists")
    target_dir = Path(target_dir).resolve()
    if target_dir == public_database.parent or public_database.parent in target_dir.parents:
        raise MigrationFailure("target_overlaps_public_database_directory")
    if target_dir / DATABASE_NAME == public_database:
        raise MigrationFailure("target_is_public_database")
    manifest = load_manifest(manifest_path)
    if file_digest(snapshot) != manifest["snapshot_sha256"]:
        raise MigrationFailure("snapshot_bytes_do_not_match")
    before = verify_database(snapshot, manifest)
    if not before["ok"]:
        return before
    output = create_private_directory(target_dir)
    destination = output / DATABASE_NAME
    online_backup(snapshot, destination)
    after = verify_database(destination, manifest)
    if not after["ok"]:
        return after
    write_private_manifest(output / MANIFEST_NAME, manifest)
    return after


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    subparsers = parser.add_subparsers(dest="command", required=True)
    snapshot = subparsers.add_parser("snapshot")
    snapshot.add_argument("--source", required=True)
    snapshot.add_argument("--output-dir", required=True)
    snapshot.add_argument("--expected-cdks", type=int)
    snapshot.add_argument("--expected-admins", type=int)
    install = subparsers.add_parser("install-new")
    install.add_argument("--snapshot", required=True)
    install.add_argument("--manifest", required=True)
    install.add_argument("--target-dir", required=True)
    install.add_argument("--public-database", required=True)
    verify = subparsers.add_parser("verify")
    verify.add_argument("--database", required=True)
    verify.add_argument("--manifest", required=True)
    verify.add_argument("--phase", choices=("exact", "running"), default="exact")
    args = parser.parse_args(argv)
    os.umask(0o077)
    try:
        if args.command == "snapshot":
            result = make_snapshot(args.source, args.output_dir,
                                   args.expected_cdks, args.expected_admins)
        elif args.command == "install-new":
            result = install_new(args.snapshot, args.manifest,
                                 args.target_dir, args.public_database)
        else:
            result = verify_database(args.database, load_manifest(args.manifest), args.phase)
    except MigrationFailure as error:
        result = {"ok": False, error.flag: True}
    except FileNotFoundError:
        result = {"ok": False, "required_path_missing": True}
    except FileExistsError:
        result = {"ok": False, "target_already_exists": True}
    except PermissionError:
        result = {"ok": False, "filesystem_permission_denied": True}
    except (sqlite3.Error, ValueError, KeyError, TypeError, OSError):
        # Exception messages may contain SQL or input data; do not emit them.
        result = {"ok": False, "migration_or_validation_failed": True}
    print(json.dumps(result, ensure_ascii=True, separators=(",", ":")))
    return 0 if result["ok"] else 1


if __name__ == "__main__":
    sys.exit(main())
