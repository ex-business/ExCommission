"""Create two local password accounts without printing secrets or overwriting existing ones."""
from pathlib import Path
import hashlib,json,secrets
root=Path(__file__).resolve().parents[1]
local=root/'.local';local.mkdir(exist_ok=True)
accounts=local/'login-accounts.json';credentials=local/'login-credentials.json'
if accounts.exists() or credentials.exists():
    if not accounts.exists() or not credentials.exists():
        raise SystemExit('登录配置不完整，拒绝覆盖已有密码文件')
    print('已有本地登录配置，未覆盖')
else:
    hashes=[];plain=[]
    for name in ('operator','approver'):
        password=secrets.token_urlsafe(18);salt=secrets.token_bytes(16)
        hashes.append({'username':name,'salt':salt.hex(),'hash':hashlib.pbkdf2_hmac('sha256',password.encode(),salt,600000).hex()})
        plain.append({'username':name,'password':password})
    for file,value in ((accounts,hashes),(credentials,plain)):
        with open(file,'x',opener=lambda path,flags:__import__('os').open(path,flags,0o600)) as f:
            json.dump(value,f,ensure_ascii=False,indent=2);f.write('\n')
    print('本地双账号已初始化，密码仅在.local/login-credentials.json，未输出')
external=local/'external-identity.json'
if not external.exists():
    with open(external,'x',opener=lambda path,flags:__import__('os').open(path,flags,0o600)) as f:f.write('{}\n')
