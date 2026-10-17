"""Provision local operator/approver TOTP without printing secrets or overwriting accounts."""
from pathlib import Path
import base64,json,os,secrets
from urllib.parse import urlencode,quote
root=Path(__file__).resolve().parents[1]
env={line.split('=',1)[0]:line.split('=',1)[1] for line in (root/'.env').read_text().splitlines() if '=' in line and not line.startswith('#')}
local=root/'.local';local.mkdir(exist_ok=True)
accounts=local/'totp-accounts.json';docker=local/'totp.env'
if accounts.exists():
    entries=json.loads(accounts.read_text())
else:
    entries=[]
    for subject in ('local-operator','local-approver'):
        secret=base64.b32encode(secrets.token_bytes(20)).decode().rstrip('=')
        tenant=env.get('LOCAL_TENANT','local-sandbox');app=env.get('LOCAL_APP','commission')
        uri='otpauth://totp/'+quote('ExCommission:'+subject,safe='')+'?'+urlencode({'secret':secret,'issuer':'ExCommission','algorithm':'SHA1','digits':6,'period':30})
        entries.append({'tenant_id':tenant,'app_id':app,'subject_id':subject,'secret':secret,'provisioning_uri':uri})
    with open(accounts,'x',opener=lambda p,f:os.open(p,f,0o600)) as out:json.dump(entries,out,ensure_ascii=False,indent=2);out.write('\n')
if not docker.exists():
    with open(docker,'x',opener=lambda p,f:os.open(p,f,0o600)) as out:out.write('AUTH_TOTP_SECRETS='+json.dumps(entries,ensure_ascii=False,separators=(',',':'))+'\n')
if 'AUTH_TOTP_FILE' not in env:
    with open(root/'.env','a') as out:out.write('\nAUTH_TOTP_FILE='+str(accounts)+'\n')
print('谷歌验证本地配置已就绪；管理员须使用受保护配置中的 provisioning_uri 绑定验证器。种子未输出，既有配置未覆盖。')
