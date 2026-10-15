"""Verify an isolated local MySQL backup, restore and transactional recovery.

Only the current Compose project's API/worker are paused. A fresh temporary
restore database and a synthetic test tenant are removed in finally blocks.
No production destination, external payment or unrelated Docker cleanup exists.
"""
from pathlib import Path
import hashlib
import json
import os
import re
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
LOCAL = ROOT / '.local'
LOCAL.mkdir(exist_ok=True)
TENANT = 'test-restore-' + uuid.uuid4().hex
RESTORE_DB = 'excommission_restore_' + uuid.uuid4().hex[:16]
ENV = os.environ.copy()
ENV.update(RESTORE_TENANT=TENANT)


def run(args, *, input_data=None, stdin=None, capture=True, check=True):
    return subprocess.run(args, cwd=ROOT, env=ENV, input=input_data, stdin=stdin,
                          stdout=subprocess.PIPE if capture else None,
                          stderr=subprocess.PIPE if capture else None, check=check)


def sql(query):
    # Password is expanded only inside the MySQL container; never passed as argv.
    return run(['docker', 'compose', 'exec', '-T', 'mysql', 'sh', '-c',
                'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot --batch --skip-column-names',
                ], input_data=(query + '\n').encode()).stdout


def go(mode, database='excommission'):
    ENV.update(RESTORE_MODE=mode, RESTORE_DATABASE=database)
    result = run(['docker', 'compose', '--profile', 'verify', 'run', '--rm',
                  '--entrypoint', 'go', '-e', 'RESTORE_MODE', '-e', 'RESTORE_DATABASE',
                  '-e', 'RESTORE_TENANT', '--volume', str(ROOT / 'internal') + ':/src/internal:ro',
                  '--volume', str(ROOT / 'conformance') + ':/src/conformance:ro',
                  'database-check', 'test', '-v', '-run', '^TestLocalRestoreDrill$', './internal/runtime'],
                 check=False)
    if result.returncode:
        raise RuntimeError('isolated restore test failed: ' + result.stdout.decode(errors='replace')[-3000:])
    return result.stdout.decode(errors='replace')


def fingerprint(database):
    if database != 'excommission' and not re.fullmatch(r'excommission_restore_[a-f0-9]{16}', database):
        raise ValueError('invalid local restore database')
    tables = sql("SELECT TABLE_NAME FROM information_schema.tables WHERE TABLE_SCHEMA='" + database +
                 "' AND TABLE_TYPE='BASE TABLE' ORDER BY TABLE_NAME").decode().splitlines()
    if not tables or any(not re.fullmatch(r'[a-z_]+', table) for table in tables):
        raise RuntimeError('unexpected restore schema')
    counts = sql(' UNION ALL '.join("SELECT '" + table + "',COUNT(*) FROM `" + database + '`.`' + table + '`'
                                   for table in tables))
    digests = {}
    for table, order in {'schema_migrations': 'version', 'ledger_entitlements': 'tenant_id,app_id,entitlement_id',
                         'ledger_journals': 'tenant_id,app_id,journal_id',
                         'payment_orders': 'tenant_id,app_id,payment_id',
                         'calculation_tasks': 'tenant_id,app_id,task_id',
                         'transaction_outbox': 'tenant_id,app_id,event_id'}.items():
        data = sql('SELECT * FROM `' + database + '`.`' + table + '` ORDER BY ' + order)
        digests[table] = hashlib.sha256(data).hexdigest()
    return {'tables': len(tables), 'row_counts_hash': hashlib.sha256(counts).hexdigest(), 'digests': digests}


seeded = paused = created = granted = False
started = time.monotonic()
try:
    seeded = True
    go('seed')
    run(['docker', 'compose', 'stop', 'api', 'worker'])
    paused = True
    baseline = fingerprint('excommission')
    with tempfile.TemporaryDirectory(prefix='restore-drill-', dir=LOCAL) as temporary:
        backup = Path(temporary) / 'snapshot.sql'
        dump = run(['docker', 'compose', 'exec', '-T', 'mysql', 'sh', '-c',
                    'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysqldump -uroot --single-transaction --routines --events --triggers --hex-blob --set-gtid-purged=OFF excommission'])
        backup.write_bytes(dump.stdout)
        backup.chmod(0o600)
        backup_hash = hashlib.sha256(dump.stdout).hexdigest()
        sql('CREATE DATABASE `' + RESTORE_DB + '` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin;')
        created = True
        sql("GRANT ALL PRIVILEGES ON `" + RESTORE_DB + "`.* TO 'excommission'@'%';")
        granted = True
        with backup.open('rb') as stream:
            run(['docker', 'compose', 'exec', '-T', 'mysql', 'sh', '-c',
                 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot "$1"', 'restore', RESTORE_DB], stdin=stream)
        restored = fingerprint(RESTORE_DB)
        if baseline != restored:
            raise RuntimeError('restored row counts or financial digests do not match')
        copied_seconds = round(time.monotonic() - started, 3)
        go('verify', RESTORE_DB)
        # Verification must not change the original UNKNOWN payment or its reservations.
        original = sql("SELECT status FROM excommission.payment_orders WHERE tenant_id='" + TENANT + "';").decode().strip()
        if original != 'UNKNOWN':
            raise RuntimeError('restore verification changed source payment')
        evidence = {'status': 'PASS_LOCAL_ONLY', 'tables': baseline['tables'],
                    'row_counts_and_financial_digests_match': True,
                    'dump_sha256': backup_hash, 'copied_and_checked_seconds': copied_seconds,
                    'recovery': 'original merchant ID query; signed callback replay; event replay; balance conservation',
                    'source_payment_unchanged': True, 'temporary_backup_retained': False,
                    'production_rpo_rto': 'UNSPECIFIED_NOT_ACCEPTED'}
        (LOCAL / 'task14-restore-evidence.json').write_text(json.dumps(evidence, ensure_ascii=False, indent=2) + '\n')
        print(json.dumps(evidence, ensure_ascii=False))
finally:
    cleanup_errors = []
    for enabled, operation in [(granted, lambda: sql("REVOKE ALL PRIVILEGES ON `" + RESTORE_DB + "`.* FROM 'excommission'@'%';")),
                               (created, lambda: sql('DROP DATABASE `' + RESTORE_DB + '`;')),
                               (seeded, lambda: go('cleanup')),
                               (paused, lambda: run(['docker', 'compose', 'up', '-d', 'api', 'worker']))]:
        if enabled:
            try:
                operation()
            except Exception as error:
                cleanup_errors.append(type(error).__name__)
    if cleanup_errors:
        raise RuntimeError('local restore cleanup needs attention: ' + ','.join(cleanup_errors))
