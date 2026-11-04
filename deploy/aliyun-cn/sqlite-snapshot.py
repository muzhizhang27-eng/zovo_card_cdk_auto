#!/usr/bin/env python3
"""SQLite backup and typed fingerprints, without printing customer/CDK values.

fingerprint: Python >=3.6. backup: Python >=3.7 (sqlite3.Connection.backup).
Source connections always use mode=ro and query_only=ON. Online backup is valid
for one database only; freeze BOTH CDK applications before the final two-file
copy. No service controls, source database mutations or provider requests are
made. The destination uses DELETE journaling so it is a self-contained file.
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import struct
import sys
import time
from urllib.parse import quote

FORMAT = 'lanting-sqlite-typed-sha256-v1'


class SnapshotError(Exception):
    pass


def canonical(value):
    return json.dumps(value, sort_keys=True, ensure_ascii=False,
                      separators=(',', ':')).encode('utf-8')


def quoted_identifier(name):
    return '"' + name.replace('"', '""') + '"'


def decoded(value):
    return value.decode('utf-8') if isinstance(value, bytes) else value


def read_only(path, timeout=5):
    path = Path(path).resolve()
    if not path.is_file():
        raise SnapshotError('Explicit existing SQLite file required')
    connection = sqlite3.connect('file:' + quote(str(path), safe='/') + '?mode=ro',
                                 uri=True, timeout=timeout, isolation_level=None)
    connection.text_factory = bytes
    connection.execute('PRAGMA query_only=ON')
    return connection


def encoded_field(storage_type, value):
    storage_type = decoded(storage_type)
    if storage_type == 'null' and value is None:
        return b'N'
    if storage_type == 'integer' and isinstance(value, int):
        payload = str(value).encode('ascii')
        return b'I' + struct.pack('>Q', len(payload)) + payload
    if storage_type == 'real' and isinstance(value, float):
        return b'R' + struct.pack('>d', value)
    if storage_type in ('text', 'blob') and isinstance(value, bytes):
        return (b'T' if storage_type == 'text' else b'B') + struct.pack('>Q', len(value)) + value
    raise SnapshotError('Unexpected SQLite storage class/value combination')


def row_digest(pairs):
    digest = hashlib.sha256(b'LTSQLITEROW1\x00')
    if len(pairs) % 2:
        raise SnapshotError('Incomplete typed row')
    for offset in range(0, len(pairs), 2):
        digest.update(encoded_field(pairs[offset], pairs[offset + 1]))
    return digest.digest()


def file_sha256(path):
    digest = hashlib.sha256()
    with Path(path).open('rb') as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


def fingerprint(path, max_rows=1000000):
    connection = read_only(path)
    try:
        connection.execute('BEGIN')
        checks = [decoded(row[0]) for row in connection.execute('PRAGMA quick_check')]
        if checks != ['ok']:
            raise SnapshotError('SQLite quick_check failed; details are not printed')
        foreign_violations = sum(1 for _ in connection.execute('PRAGMA foreign_key_check'))
        if foreign_violations:
            raise SnapshotError('SQLite foreign_key_check found violations')
        settings = {}
        for key in ('encoding', 'user_version', 'application_id', 'auto_vacuum'):
            settings[key] = decoded(connection.execute('PRAGMA ' + key).fetchone()[0])
        journal_mode = decoded(connection.execute('PRAGMA journal_mode').fetchone()[0])
        objects = []
        tables = []
        for kind, name, table, sql in connection.execute(
                'SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type COLLATE BINARY,name COLLATE BINARY'):
            kind, name, table = decoded(kind), decoded(name), decoded(table)
            # Only schema digests are emitted; defaults/SQL text might be private.
            sql_bytes = sql if sql is not None else b''
            objects.append({'type': kind, 'name': name, 'table': table,
                            'sql_is_null': sql is None,
                            'sql_sha256': hashlib.sha256(sql_bytes).hexdigest()})
            if kind == 'table':
                if sql_bytes.lstrip().upper().startswith(b'CREATE VIRTUAL TABLE'):
                    raise SnapshotError('Virtual tables require a separate extension audit')
                tables.append(name)
        if not tables:
            raise SnapshotError('Empty database schema is not accepted')
        records = []
        for name in sorted(tables, key=lambda value: value.encode('utf-8')):
            quoted = quoted_identifier(name)
            # table_xinfo includes generated/hidden columns when available.
            raw_columns = list(connection.execute('PRAGMA table_xinfo(' + quoted + ')'))
            if not raw_columns:
                raw_columns = list(connection.execute('PRAGMA table_info(' + quoted + ')'))
            if not raw_columns:
                raise SnapshotError('Cannot inspect table columns')
            columns = []
            for row in raw_columns:
                cid, column, declared, required, default, primary = row[:6]
                columns.append({'cid': cid, 'name': decoded(column),
                                'declared_type': decoded(declared), 'not_null': required,
                                'default_is_null': default is None,
                                'default_sql_sha256': hashlib.sha256(default or b'').hexdigest(),
                                'primary_key_position': primary,
                                'hidden': row[6] if len(row) > 6 else 0})
            expressions = []
            for column in columns:
                identifier = quoted_identifier(column['name'])
                expressions.extend(('typeof(' + identifier + ')', identifier))
            rows = []
            for row in connection.execute('SELECT ' + ','.join(expressions) + ' FROM ' + quoted):
                if len(rows) >= max_rows:
                    raise SnapshotError('Row digest memory bound exceeded')
                rows.append(row_digest(row))
            rows.sort()
            content = hashlib.sha256(b'LTSQLITETABLE1\x00')
            for digest in rows:
                content.update(digest)
            records.append({'name': name, 'row_count': len(rows),
                            'column_count': len(columns),
                            'column_metadata_sha256': hashlib.sha256(canonical(columns)).hexdigest(),
                            'content_sha256': content.hexdigest()})
        schema = {'database_settings': settings, 'schema_objects': objects}
        schema_sha = hashlib.sha256(canonical(schema)).hexdigest()
        return {'format': FORMAT, 'path': str(Path(path).resolve()),
                'sqlite_library_version': sqlite3.sqlite_version,
                'generated_at_utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                'transaction': 'read-only consistent read transaction',
                'quick_check': 'ok', 'foreign_key_violations': 0,
                'journal_mode': journal_mode, 'database_settings': settings,
                'schema_sha256': schema_sha, 'schema_objects': objects,
                'tables': records, 'table_count': len(records),
                'row_count': sum(table['row_count'] for table in records),
                'overall_sha256': hashlib.sha256(canonical({'schema_sha256': schema_sha, 'tables': records})).hexdigest()}
    finally:
        if connection.in_transaction:
            connection.rollback()
        connection.close()


def comparison(actual, expected):
    if expected.get('format') != FORMAT:
        raise SnapshotError('Unsupported comparison format')
    old = {table['name']: table for table in expected['tables']}
    new = {table['name']: table for table in actual['tables']}
    differences = [name for name in sorted(set(old) | set(new)) if old.get(name) != new.get(name)]
    match = (not differences and actual['schema_sha256'] == expected['schema_sha256']
             and actual['overall_sha256'] == expected['overall_sha256']
             and actual['database_settings'] == expected['database_settings'])
    return {'match': match, 'different_tables': differences,
            'schema_match': actual['schema_sha256'] == expected['schema_sha256'],
            'journal_modes_match': actual['journal_mode'] == expected['journal_mode'],
            'sqlite_library_versions_match': actual['sqlite_library_version'] == expected['sqlite_library_version']}


def backup(source, destination, timeout=120, max_rows=1000000):
    if not hasattr(sqlite3.Connection, 'backup'):
        raise SnapshotError('Backup requires Python 3.7+; fingerprint supports Python 3.6')
    source_path = Path(source).resolve()
    destination_path = Path(destination).absolute()
    if source_path == destination_path.resolve():
        raise SnapshotError('Source and backup must be different files')
    # Exclusive creation refuses existing files and symlinks, avoiding overwrite.
    descriptor = os.open(str(destination_path), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    os.close(descriptor)
    src, dst = None, None
    complete = False
    started = time.monotonic()
    try:
        src = read_only(source_path)
        dst = sqlite3.connect(str(destination_path), timeout=5)
        def progress(status, remaining, total):
            if time.monotonic() - started > timeout:
                raise SnapshotError('Online backup deadline exceeded')
        # sqlite3 backup API provides a consistent snapshot, including uncheckpointed WAL.
        src.backup(dst, pages=256, progress=progress, sleep=0.05)
        # This changes ONLY the new destination, never the read-only source.
        # A copied WAL header without sidecars cannot be read on every SQLite
        # build in mode=ro; DELETE makes the transfer artifact self-contained.
        if dst.execute('PRAGMA journal_mode=DELETE').fetchone()[0].lower() != 'delete':
            raise SnapshotError('Cannot make the new backup file self-contained')
        dst.close()
        dst = None
        # Some SQLite builds leave a stale SHM after leaving WAL mode. DELETE
        # is confirmed above and this new destination has no other users.
        for suffix in ('-wal', '-shm'):
            sidecar = Path(str(destination_path) + suffix)
            if sidecar.exists():
                if suffix == '-wal' and sidecar.stat().st_size:
                    raise SnapshotError('New backup unexpectedly retains WAL content')
                sidecar.unlink()
        src.close()
        src = None
        report = fingerprint(destination_path, max_rows=max_rows)
        report['backup'] = {'source_path': str(source_path), 'destination_path': str(destination_path),
                            'bytes': destination_path.stat().st_size,
                            'file_sha256': file_sha256(destination_path),
                            'mode': oct(destination_path.stat().st_mode & 0o777),
                            'destination_journal_mode': 'delete',
                            'mechanism': 'sqlite3 online backup API from read-only connection'}
        complete = True
        return report
    finally:
        if dst is not None:
            dst.close()
        if src is not None:
            src.close()
        if not complete:
            # Remove only the exclusive destination newly created by this call.
            for suffix in ('', '-journal', '-wal', '-shm'):
                try:
                    Path(str(destination_path) + suffix).unlink()
                except FileNotFoundError:
                    pass


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('fingerprint', 'backup'))
    parser.add_argument('source', help='Explicit SQLite database path')
    parser.add_argument('--output', help='New backup file path; existing files are refused')
    parser.add_argument('--compare', help='Expected fingerprint JSON')
    parser.add_argument('--max-row-digests', type=int, default=1000000)
    parser.add_argument('--backup-timeout', type=float, default=120)
    args = parser.parse_args()
    if args.max_row_digests <= 0 or args.backup_timeout <= 0:
        raise SnapshotError('Positive resource bounds required')
    if args.action == 'backup':
        if not args.output:
            raise SnapshotError('Backup requires a new --output file')
        result = backup(args.source, args.output, args.backup_timeout, args.max_row_digests)
    else:
        if args.output:
            raise SnapshotError('--output is only used for backup files')
        result = fingerprint(args.source, args.max_row_digests)
    code = 0
    if args.compare:
        with open(args.compare, 'r', encoding='utf-8') as handle:
            expected = json.load(handle)
        result['comparison'] = comparison(result, expected)
        code = 0 if result['comparison']['match'] else 1
    print(json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True))
    return code


if __name__ == '__main__':
    try:
        sys.exit(main())
    except SnapshotError as error:
        sys.stderr.write('SQLite snapshot failed: {}.\n'.format(error))
        sys.exit(2)
    except (OSError, sqlite3.Error, ValueError, KeyError, TypeError, UnicodeDecodeError):
        # Never print SQL errors or row/schema values that might contain secrets.
        sys.stderr.write('SQLite snapshot failed; no result accepted. Raw database details suppressed.\n')
        sys.exit(2)
