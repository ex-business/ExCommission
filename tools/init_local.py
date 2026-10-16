"""生成仅本地使用的凭据和空沙箱来源；不覆盖现有值，不输出凭据。"""
from pathlib import Path
import secrets
root = Path(__file__).resolve().parents[1]
env = root / '.env'
content = env.read_text() if env.exists() else ''
for key in ('MYSQL_ROOT_PASSWORD', 'MYSQL_PASSWORD', 'LOCAL_OPERATOR_TOKEN', 'LOCAL_APPROVER_TOKEN', 'LOCAL_CURSOR_KEY', 'LOCAL_SOURCE_KEY', 'LOCAL_PAYMENT_KEY', 'LOCAL_REGISTRATION_KEY', 'LOCAL_QUALIFICATION_KEY'):
    if not any(line.startswith(key + '=') for line in content.splitlines()):
        content += key + '=' + secrets.token_hex(32) + '\n'
if not any(line.startswith('MYSQL_PORT=') for line in content.splitlines()):
    content += 'MYSQL_PORT=13316\n'
env.write_text(content)
env.chmod(0o600)
local = root / '.local'
local.mkdir(exist_ok=True)
users = local / 'users.json'
if not users.exists():
    users.write_text('[]\n')
statistics = local / 'grade-statistics.json'
if not statistics.exists():
    statistics.write_text('{}\n')
print('本地配置已就绪；凭据仅保存在被忽略的 .env，未输出。')

payment_results = local / 'payment-results.json'
if not payment_results.exists():
    payment_results.write_text('{"queries":{},"reconciliations":{}}\n')

recovery_evidence = local / 'recovery-evidence.json'
if not recovery_evidence.exists():
    recovery_evidence.write_text('{}\n')

# 初始化独立密码账号，不打印或覆盖已有凭据。
import runpy
runpy.run_path(str(root / "tools/init_login.py"), run_name="__main__")
