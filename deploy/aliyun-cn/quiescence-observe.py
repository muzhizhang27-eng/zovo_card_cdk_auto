#!/usr/bin/env python3
"""Read-only SG CDK drain evidence. Never contacts any provider or changes services.

Output contains only fixed route/status groups, timestamps, counts, PID/FD/queue
metadata. No request query, customer address, CDK, session, API key or log body.
One snapshot is not a drain certificate; compare repeated snapshots after the
operator has frozen all HTTP entry points.
"""
import argparse
import collections
import datetime as dt
import gzip
import ipaddress
import json
from pathlib import Path
import re
import sqlite3
import subprocess
import time

SERVICES = ('cdk-recharge.service', 'cdk-admin.service', 'cdk-lookup.service')
LOGS = {'recharge': '/var/log/nginx/cdk-redemption-access.log', 'admin': '/var/log/nginx/cdk-admin-access.log'}
DATABASES = {'recharge': '/var/lib/cdk-recharge/cdk_recharge.db', 'admin': '/var/lib/cdk-admin/cdk_recharge.db'}
STATUS = ('pending', 'submitted', 'queued', 'running', 'dispatching', 'processing', 'awaiting_card', 'funding_pending', 'card_open_review', 'review', 'reserved', 'completed', 'success', 'paid', 'failed', 'failed_precharge', 'declined', 'cancelled', 'rejected', 'unused', 'active', 'consumed', 'used', 'redeemed', 'disabled', 'frozen', 'expired', 'skipped')
ROUTES = {
    '/health': 'health', '/api/v1/public/cdk/preview': 'preview',
    '/api/v1/public/cdk/preflight': 'preflight', '/api/v1/public/cdk/redeem': 'redeem',
    '/api/v1/public/cdk/recover-subscription': 'recover_subscription',
    '/api/v1/public/cdk/exchange': 'agent_exchange',
    '/api/v1/public/cdk/result': 'result_poll', '/api/v1/public/cdk/result-by-code': 'result_poll',
    '/api/v1/webhooks/cardplatform': 'provider_webhook',
    '/api/v1/admin/cardplatform/cdks': 'admin_cdk_inventory_or_issue',
    '/api/v1/admin/cardplatform/cdks/sync': 'admin_inventory_sync',
    '/api/v1/admin/cardplatform/cdks/store': 'admin_inventory_store',
    '/api/v1/admin/cardplatform/cdks/batch-disable': 'admin_cdk_disable',
    '/api/v1/admin/cardplatform/cdks/batch-enable': 'admin_cdk_enable',
    '/api/v1/admin/cardplatform/cdks/batch-note': 'admin_cdk_note',
    '/api/v1/admin/cardplatform/cdks/batch-clear-note': 'admin_cdk_note',
    '/api/v1/admin/cardplatform/cdk-orders': 'admin_order_read',
    '/api/v1/admin/card-selection/sync': 'admin_plan_sync',
    '/api/v1/admin/card-health/observe': 'admin_card_health_observe',
    '/api/v1/admin/card-health/unblock': 'admin_card_health_unblock',
    '/api/v1/admin/system/update': 'admin_update',
    '/api/v1/public/billing/check': 'billing_check', '/api/v1/billing/check': 'billing_check',
}


def utc(timestamp):
    return dt.datetime.fromtimestamp(timestamp, dt.timezone.utc).isoformat()


def route_group(path, method):
    path = str(path).split('?', 1)[0].split('#', 1)[0]
    if path in ROUTES:
        group = ROUTES[path]
        return 'admin_issue' if group == 'admin_cdk_inventory_or_issue' and method == 'POST' else group
    for pattern, group in ((r'/api/v1/admin/cardplatform/cdks/[^/]+/disable', 'admin_cdk_disable'),
                           (r'/api/v1/admin/cardplatform/cdks/[^/]+/enable', 'admin_cdk_enable'),
                           (r'/api/v1/admin/cardplatform/cdks/[^/]+/note', 'admin_cdk_note'),
                           (r'/api/v1/admin/cardplatform/cdk-orders/[^/]+', 'admin_order_read'),
                           (r'/api/v1/admin/cardplatform/cards/[^/]+', 'admin_card_delete_or_read')):
        if re.fullmatch(pattern, path):
            return group
    if path.startswith('/api/v1/admin/cardplatform/'):
        return 'admin_provider_other'
    if path.startswith('/api/v1/admin/'):
        return 'admin_other'
    if path.startswith('/api/v1/lookup/'):
        return 'lookup'
    if path.startswith('/api/v1/auth/'):
        return 'authentication'
    if path.startswith('/api/'):
        return 'other_api'
    if path.startswith('/assets/'):
        return 'static_asset'
    if path in ('/', '/recharge', '/ops/login', '/ops/cdkeys'):
        return 'frontend_page'
    return 'other_path'


def nginx_aggregate(prefix, cutoff, now):
    groups = {}
    scanned = malformed = inside = 0
    files = list(Path(prefix).parent.glob(Path(prefix).name + '*'))
    for path in files:
        if not path.is_file():
            continue
        opener = gzip.open if path.suffix == '.gz' else open
        try:
            with opener(path, 'rt', errors='replace') as source:
                for line in source:
                    scanned += 1
                    try:
                        item = json.loads(line)
                        when = dt.datetime.fromisoformat(item['time'].replace('Z', '+00:00')).timestamp()
                        if when < cutoff or when > now + 5:
                            continue
                        method = str(item.get('method', '')).upper()
                        if method not in ('GET', 'POST', 'PUT', 'DELETE', 'HEAD', 'OPTIONS', 'PATCH'):
                            method = 'OTHER'
                        status = int(item['status'])
                        if not 100 <= status <= 599:
                            status = 0
                        key = (method, route_group(item.get('path', ''), method), status)
                        group = groups.setdefault(key, {'count': 0, 'last_completed_unix': 0, 'max_request_seconds': 0})
                        group['count'] += 1
                        group['last_completed_unix'] = max(group['last_completed_unix'], when)
                        group['max_request_seconds'] = max(group['max_request_seconds'], float(item.get('seconds', 0) or 0))
                        inside += 1
                    except (ValueError, TypeError, KeyError, OverflowError):
                        malformed += 1
        except OSError:
            malformed += 1
    result = []
    for (method, route, status), values in sorted(groups.items()):
        when = values.pop('last_completed_unix')
        result.append({'method': method, 'route_group': route, 'status': status, **values,
                       'last_completed_utc': utc(when), 'last_completed_age_seconds': round(now - when, 3)})
    return {'files_scanned': len(files), 'lines_scanned': scanned, 'unparsed_lines': malformed,
            'requests_in_window': inside, 'groups': result,
            'limitation': 'access log records completed requests only; active requests may not yet appear'}


def grouped_status(connection, table, column):
    placeholders = ','.join('?' for _ in STATUS)
    query = (f'SELECT CASE WHEN lower(COALESCE({column},\'\')) IN ({placeholders}) '
             f'THEN lower({column}) ELSE \'other_or_empty\' END, COUNT(*) FROM {table} GROUP BY 1')
    return dict(connection.execute(query, STATUS))


def database_metadata(path, now):
    connection = sqlite3.connect('file:' + path + '?mode=ro', uri=True, timeout=2)
    connection.execute('PRAGMA query_only=ON')
    connection.execute('BEGIN')
    result = {'tables': {}}
    definitions = {
        'recharge_tasks': ('updated_at', 'task_status'),
        'cardplatform_cdk_codes': ('created_at', 'status'),
        'cdk_session_bindings': ('updated_at', None),
        'webhook_events': ('created_at', None),
        'agent_cdk_exchanges': ('created_at', 'order_status'),
        'card_fail_events': ('created_at', 'order_status'),
        'card_blocklist': ('blocked_at', 'freeze_status'),
        'admin_audit_logs': ('created_at', None),
        'plan_status_cache': ('synced_at', None),
        'card_product_cache': ('synced_at', None),
    }
    try:
        present = {row[0] for row in connection.execute("SELECT name FROM sqlite_master WHERE type='table'")}
        for table, (timestamp, status) in definitions.items():
            if table not in present:
                result['tables'][table] = {'missing': True}
                continue
            count, last = connection.execute(f"SELECT COUNT(*), MAX(strftime('%s',{timestamp})) FROM {table}").fetchone()
            info = {'count': count, 'latest_utc': utc(int(last)) if last else None,
                    'latest_age_seconds': round(now - int(last), 3) if last else None}
            if status:
                info['status_counts'] = grouped_status(connection, table, status)
            result['tables'][table] = info
        if 'cdk_session_bindings' in present:
            tokens, sessions = connection.execute("SELECT SUM(CASE WHEN length(COALESCE(redemption_token,''))>0 THEN 1 ELSE 0 END), SUM(CASE WHEN length(COALESCE(session_payload,''))>0 THEN 1 ELSE 0 END) FROM cdk_session_bindings").fetchone()
            result['binding_presence_counts'] = {'with_redemption_token': tokens or 0, 'with_session': sessions or 0}
        if 'site_settings' in present:
            # Only a boolean; never read or emit the webhook secret itself.
            row = connection.execute("SELECT COUNT(*) FROM site_settings WHERE key='webhook_secret' AND length(trim(value))>0").fetchone()
            result['webhook_secret_configured'] = bool(row[0])
        busy = {'pending', 'submitted', 'queued', 'running', 'dispatching', 'processing',
                'awaiting_card', 'funding_pending', 'card_open_review', 'review', 'reserved'}
        result['local_nonterminal_counts'] = {
            name: sum(count for status, count in result['tables'][table].get('status_counts', {}).items() if status in busy)
            if table in present else None
            for name, table in [('legacy_manual_tasks', 'recharge_tasks'), ('cached_cdk_status', 'cardplatform_cdk_codes')]
        }
    finally:
        connection.close()
    result['file_metadata'] = {}
    for suffix in ('', '-wal', '-shm', '-journal'):
        p = Path(path + suffix)
        if p.exists():
            st = p.stat()
            result['file_metadata'][suffix or 'database'] = {'bytes': st.st_size, 'mtime_ns': st.st_mtime_ns}
    result['limitation'] = 'local CDK statuses are cached; remote accepted orders are not an authoritative local pending queue'
    return result


def run(args):
    return subprocess.run(args, check=True, capture_output=True, text=True, timeout=12).stdout


def process_metadata():
    result, by_pid = {}, {}
    for unit in SERVICES:
        pid = int(run(['systemctl', 'show', unit, '--property=MainPID', '--value']).strip() or 0)
        item = {'pid': pid, 'running': pid > 0, 'tcp': [], 'unix_state_counts': {}, 'lookup_socket_states': {}}
        result[unit] = item
        if not pid:
            continue
        by_pid[pid] = item
        proc = Path('/proc') / str(pid)
        try:
            item['io_counters'] = {key: int(value) for key, value in (line.split(':', 1) for line in (proc / 'io').read_text().splitlines())}
            item['fd_count'] = len(list((proc / 'fd').iterdir()))
            stat = (proc / 'stat').read_text().rsplit(')', 1)[1].split()
            item['cpu_ticks'] = int(stat[11]) + int(stat[12])
            item['threads'] = int(stat[17])
        except (OSError, ValueError):
            item['proc_metadata_error'] = True
    raw = run(['ss', '-Htanpie'])
    records = []
    for line in raw.splitlines():
        if line[:1].isspace() and records:
            records[-1][1] += ' ' + line.strip()
        else:
            records.append([line, ''])
    for line, extra in records:
        owners = {int(p) for p in re.findall(r'pid=(\d+)', line)} & set(by_pid)
        if not owners:
            continue
        fields = line.split()
        if len(fields) < 5:
            continue
        state, recvq, sendq, local, peer = fields[:5]
        def endpoint(value):
            host, _, port = value.rpartition(':')
            return host.strip('[]'), int(port) if port.isdigit() else 0
        local_host, local_port = endpoint(local)
        peer_host, peer_port = endpoint(peer)
        try:
            loopback = ipaddress.ip_address(peer_host).is_loopback
        except ValueError:
            loopback = False
        category = 'listener' if state == 'LISTEN' else ('loopback' if loopback else 'external')
        ino = re.search(r'ino:(\d+)', line)
        counters = {key: int(value) for key, value in re.findall(r'\b(bytes_acked|bytes_received|bytes_sent|lastsnd|lastrcv|lastack):(\d+)', extra)}
        for pid in owners:
            row = {'state': state, 'category': category, 'local_port': local_port,
                'peer_port': peer_port, 'recv_queue_bytes': int(recvq), 'send_queue_bytes': int(sendq),
                'socket_inode': int(ino.group(1)) if ino else None, 'tcp_counters': counters}
            if state == 'LISTEN':
                row['listen_backlog_current'] = row.pop('recv_queue_bytes')
                row['listen_backlog_limit'] = row.pop('send_queue_bytes')
            if 'lastsnd' in counters and 'lastrcv' in counters:
                row['last_payload_activity_age_ms'] = min(counters['lastsnd'], counters['lastrcv'])
            by_pid[pid]['tcp'].append(row)
    for line in run(['ss', '-Hxap']).splitlines():
        owners = {int(p) for p in re.findall(r'pid=(\d+)', line)} & set(by_pid)
        fields = line.split()
        if not owners or len(fields) < 4:
            continue
        for pid in owners:
            counts = by_pid[pid]['unix_state_counts']
            counts[fields[1]] = counts.get(fields[1], 0) + 1
            if '/run/cdk-lookup/lookup.sock' in line:
                counts = by_pid[pid]['lookup_socket_states']
                counts[fields[1]] = counts.get(fields[1], 0) + 1
    for item in result.values():
        listener_ports = {row['local_port'] for row in item['tcp'] if row['state'] == 'LISTEN'}
        for row in item['tcp']:
            row['direction'] = 'listener' if row['state'] == 'LISTEN' else ('inbound_http' if row['local_port'] in listener_ports else 'outbound')
        item['tcp_state_counts'] = dict(collections.Counter(row['state'] for row in item['tcp']))
        item['queued_tcp_bytes'] = sum(row['recv_queue_bytes'] + row['send_queue_bytes'] for row in item['tcp'] if row['state'] != 'LISTEN')
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--hours', type=float, default=24)
    parser.add_argument('--since', help='ISO8601 lower bound, e.g. the confirmed maintenance freeze time')
    args = parser.parse_args()
    now = time.time()
    cutoff = dt.datetime.fromisoformat(args.since.replace('Z', '+00:00')).timestamp() if args.since else now - args.hours * 3600
    out = {'observed_utc': utc(now), 'window_start_utc': utc(cutoff),
           'maintenance_marker_exists': Path('/var/lib/cdk-migration/maintenance').exists(),
           'remote_pending_orders': {'checked': False, 'count': None, 'reason': 'operator requested no live customer/provider order API calls'},
           'automatic_safe_to_stop_verdict': False,
           'limitations': ['No remote provider requests are made.', 'No finite universal timeout exists in the old public limiter: Retry-After and queue waits are not capped.', 'Access logs are completion records; TCP ESTAB can be active or idle. Compare repeated snapshots.', 'Periodic plan sync writes caches every 180 seconds; distinguish cache writes from transaction writes.', 'Empty local recharge_tasks does not prove absence of remote Zovo orders.']}
    for key, fn in [('processes', process_metadata), ('access_logs', lambda: {name: nginx_aggregate(path, cutoff, now) for name, path in LOGS.items()}), ('databases', lambda: {name: database_metadata(path, now) for name, path in DATABASES.items()})]:
        try:
            out[key] = fn()
        except Exception as error:
            # Exception text can contain data; emit only the type and fail closed.
            out[key] = {'collection_error_type': type(error).__name__}
    print(json.dumps(out, indent=2, sort_keys=True))


if __name__ == '__main__':
    main()
