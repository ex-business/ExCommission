#!/usr/bin/env python3
# coding: utf-8
"""本机隔离进程验收：真实模拟会员、来源投递、API、worker及会员BFF；不直接写奖励/钱包表。"""
import argparse,datetime as dt,hashlib,hmac,http.cookiejar,json,os,secrets,signal,socket,sqlite3,subprocess,time,urllib.request,urllib.error,urllib.parse,uuid
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('--wallet-binary');p.add_argument('--database-config',required=True);p.add_argument('--member-binary',required=True);p.add_argument('--web-dir',required=True);p.add_argument('--output',required=True);a=p.parse_args()
root=Path(__file__).resolve().parents[1];out=Path(a.output).resolve();
if out.exists() and any(out.iterdir()):raise SystemExit('输出目录必须为空，拒绝覆盖旧租户配置和证据')
out.mkdir(parents=True,exist_ok=True,mode=0o700);os.chmod(out,0o700)
base=json.loads(Path(a.database_config).read_text());
if base['DB_ADDRESS'].split(':')[0] not in ['127.0.0.1','localhost']:raise SystemExit('仅允许本机数据库')
def save(name,v):
 q=out/name;q.parent.mkdir(parents=True,exist_ok=True,mode=0o700);q.write_text(json.dumps(v,ensure_ascii=False,indent=2));q.chmod(0o600)
def port():
 with socket.socket() as s:s.bind(('127.0.0.1',0));return s.getsockname()[1]
ports={k:port() for k in ['api','worker','member','identity','web','wallet','relay']};urls={k:'http://127.0.0.1:'+str(v) for k,v in ports.items()};tenant='fullchain-'+uuid.uuid4().hex[:12];key=lambda:secrets.token_hex(32);op,approver,reg,fact,context,identity=[key() for _ in range(6)];processes=[];evidence=[];relay=None
def stage(name,data=None):evidence.append({'stage':name});print('已完成：'+name,flush=True);save('progress.json',{'tenant':tenant,'steps':evidence});
def wait(fn,label,seconds=35):
 end=time.monotonic()+seconds
 while time.monotonic()<end:
  try:
   value=fn()
   if value:return value
  except (urllib.error.URLError,ConnectionError):pass
  time.sleep(.2)
 raise RuntimeError('等待超时：'+label)
def start(name,args,cwd,extra):
 env={k:v for k,v in os.environ.items() if not k.startswith(('AUTH_','OPS_','WEB_ENTRY_','EX_SERVICE_','EX_ROUTER_','LOCAL_','WALLET_','AFFILIATE_','MEMBER_'))};env.update(extra)
 f=os.open(out/(name+'.log'),os.O_CREAT|os.O_WRONLY|os.O_APPEND,0o600);proc=subprocess.Popen(args,cwd=cwd,env=env,stdin=subprocess.DEVNULL,stdout=f,stderr=f,start_new_session=True);os.close(f);processes.append(proc)
 def ready():
  if proc.poll() is not None:raise RuntimeError(name+'启动退出，检查私有日志')
  try:urllib.request.urlopen(urls[name]+('/readyz' if name in ['api','worker']else '/healthz'),timeout=.5);return True
  except urllib.error.HTTPError as e:return e.code in [401,403,404]
 wait(ready,name)
class Client:
 def __init__(self,base):self.base=base;self.client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()));self.csrf=''
 def call(self,path,data=None,headers=None,want=200):
  req=urllib.request.Request(self.base+path,data=None if data is None else json.dumps(data,separators=(',',':')).encode(),headers={'Origin':self.base,'Content-Type':'application/json',**(headers or{})})
  try:r=self.client.open(req,timeout=15)
  except urllib.error.HTTPError as e:r=e
  raw=r.read()
  try:v=json.loads(raw)
  except ValueError:v={'raw':raw.decode()}
  # 证据只保存操作路径、业务响应编号和状态，不保存登录Cookie/口令。
  evidence.append({'path':path.split('?')[0],'status':r.status,'request_id':v.get('request_id',r.headers.get('X-Request-ID',''))})
  if r.status==404 and path.startswith('/v1/affiliate/daily/'):return {}
  if r.status!=want:raise RuntimeError(path+': HTTP '+str(r.status)+' '+str(v.get('error',{})))
  return v.get('data',v)
api=Client(urls['api']);backend=lambda path,data=None,checker=False,want=200,headers=None:api.call(path,data,{'Authorization':'Bearer '+(approver if checker else op),'Idempotency-Key':uuid.uuid4().hex,**(headers or{})},want)
def approve(change):return backend('/v1/approvals/'+change['approval_id']+'/decisions',{'decision':'APPROVE','expected_revision':0,'object_hash':change['object_hash'],'reason':'隔离全链路独立审批'},True)
try:
 now=dt.datetime.now(dt.timezone.utc);day=now.date().isoformat();future=(now+dt.timedelta(days=1)).replace(hour=2,minute=0,second=0,microsecond=0).isoformat().replace('+00:00','Z')
 cfg={k:base[k] for k in ['DB_ADDRESS','DB_USER','MYSQL_PASSWORD']};cfg.update(APP_MODE='local',LOCAL_TENANT=tenant,LOCAL_APP='commission',LOCAL_OPERATOR_TOKEN=op,LOCAL_APPROVER_TOKEN=approver,LOCAL_CURSOR_KEY=key(),LOCAL_SOURCE_KEY=key(),LOCAL_PAYMENT_KEY=key(),LOCAL_AFFILIATE_FLOW_ENABLED='true',LOCAL_AFFILIATE_FLOW_KEY=fact,AFFILIATE_CLIENT_CONTEXT_KEY=context,LOCAL_REGISTRATION_ENABLED='true',LOCAL_REGISTRATION_KEY=reg,LOCAL_REGISTRATION_URL=urls['member']+'/',LOCAL_USERS_FILE='',AUTH_LOCAL_ACCOUNTS_FILE='',AUTH_EXTERNAL_FILE='',AUTH_TOTP_FILE='',AUTH_TOTP_SECRETS='',MEMBER_INVITATION_SOURCE='sandbox',AUTH_MEMBER_SOURCE_FILE=str(out/'member-identity.json'))
 if a.wallet_binary:
  from affiliate_wallet_relay import WalletRelay
  walletKey,callbackKey=key(),key();walletClient='affiliate-fullchain';walletSite='1000'
  save('wallet-adapter.json',{'endpoint':urls['relay'],'sites':[{'site_id':walletSite,'tenant_id':tenant,'app_id':'commission','client_id':walletClient,'key':walletKey,'callback_key':callbackKey}]})
  cfg.update(AFFILIATE_CLAIM_WALLET='wallet-sandbox',WALLET_SANDBOX_CONFIG_FILE=str(out/'wallet-adapter.json'))
  save('wallet-config.json',{'listen':'127.0.0.1:'+str(ports['wallet']),'ops_url':'http://127.0.0.1:19587','ops_secret':key(),'sites':[{'site_id':walletSite,'client_id':walletClient,'key':walletKey,'callback_key':callbackKey,'callback_url':urls['relay']+'/v1/payment-callbacks/affiliate-wallet/'+walletClient}]})
  start('wallet',[a.wallet_binary,'-config',str(out/'wallet-config.json'),'-db',str(out/'wallet.db')],root,{})
  relay=WalletRelay(('127.0.0.1',ports['relay']),urls['wallet'],urls['api'])
 save('member-identity.json',{'endpoint':urls['identity']+'/integration/introspect','client_id':'fullchain','client_secret':identity,'issuer':'fullchain-member','audience':'commission','source_id':'sandbox','local_sandbox':True})
 for role in ['api','worker']:
  d=out/role;d.mkdir(exist_ok=True);save(role+'/'+role+'-runtime.json',{**cfg,'HTTP_ADDR':'127.0.0.1:'+str(ports[role])});(d/'business-clock.txt').write_text('REAL')
  start(role,[str(root/'.local/fullchain/runtime.test'),'-test.run=^TestAffiliateAcceptanceProcess$','-test.timeout=0'],root,{'AFFILIATE_PROCESS_ACCEPTANCE_DIR':str(d),'AFFILIATE_PROCESS_ACCEPTANCE_ROLE':role})
 condition={'deposit_minor':'100','valid_bet_minor':'100'};config={'enabled':True,'currency':'USD','currency_scale':2,'timezone':'UTC','grade_period':'DAY','grade_condition':condition,'referral_condition':condition,'invitee_condition':condition,'invitee_reward_minor':'50','levels':[{'level':0,'qualified_members':0,'daily_valid_bet_minor':'0','rates_ppm':[4000,2500,1500]}],'steps':[{'step':1,'additional_members':1,'reward_minor':'100'}],'venue_type_ids':['SPORTS']}
 approve(backend('/v1/affiliate/config-requests',{'config':config,'expected_revision':0,'reason':'本机隔离全链路方案'},want=202))
 save('simulation.json',{'url':urls['api'],'token':op,'key':fact,'tenant':tenant,'app':'commission','source':'sandbox','member_database':str(out/'members.db'),'database':str(out/'source.db')})
 save('member.json',{'Listen':'127.0.0.1:'+str(ports['member']),'PublicURL':urls['member']+'/','Database':str(out/'members.db'),'CommissionURL':urls['api'],'CommissionToken':op,'RegistrationKey':reg,'Tenant':tenant,'App':'commission','Source':'sandbox','IdentityListen':'127.0.0.1:'+str(ports['identity']),'IdentityClientID':'fullchain','IdentitySecret':identity,'IdentityIssuer':'fullchain-member','IdentityAudience':'commission','MemberPortalURL':urls['web'],'SimulationSourceFile':str(out/'simulation.json')})
 start('member',[a.member_binary,'-config',str(out/'member.json')],root,{})
 save('web-source.json',{'login_url':urls['identity']+'/integration/login','revoke_url':urls['identity']+'/integration/revoke','client_id':'fullchain','client_secret':identity,'local_sandbox':True})
 start('web',['node','server/index.mjs'],Path(a.web_dir),{'LOCAL_TENANT':tenant,'LOCAL_APP':'commission','PORT':str(ports['web']),'BACKEND_URL':urls['api'],'LOCAL_OPERATOR_TOKEN':op,'LOCAL_APPROVER_TOKEN':approver,'LOCAL_SOURCE_KEY':cfg['LOCAL_SOURCE_KEY'],'WEB_OPERATOR_PASSWORD':key(),'WEB_APPROVER_PASSWORD':key(),'MEMBER_SOURCE_FILE':str(out/'web-source.json'),'AFFILIATE_CLIENT_CONTEXT_KEY':context,'OPS_IDENTITY_CONFIG_FILE':'','EX_SERVICE_GATEWAY_CONFIG_FILE':''})
 password=key();parent=Client(urls['member']);child=Client(urls['member']);parentEmail=tenant+'-parent@example.test';childEmail=tenant+'-child@example.test'
 parent.call('/api/register',{'email':parentEmail,'display_name':'全链路推荐人','password':password},want=201);wait(lambda:parent.call('/api/me').get('delivery_status')=='SUCCEEDED','推荐人投递');invite=parent.call('/api/invitation',{})['invitation_url'];code=urllib.parse.parse_qs(urllib.parse.urlparse(invite).query)['ic'][0]
 child.call('/api/register',{'email':childEmail,'display_name':'全链路受邀会员','password':password,'invitation_code':code},want=201);wait(lambda:child.call('/api/me').get('delivery_status')=='SUCCEEDED','受邀投递');stage('邀请注册与关系绑定')
 for suffix,kind,amount,valid,venue in [('deposit','DEPOSIT_SUCCEEDED','10.00','0',''),('bet','BET_SETTLED','100.00','100.00','SPORTS')]:child.call('/api/simulation/events',{'business_id':tenant+'-'+suffix,'kind':kind,'amount':amount,'valid_bet':valid,'venue_type_id':venue},want=202)
 wait(lambda:len(child.call('/api/simulation/events')['items'])==2 and all(x['state']=='DELIVERED'for x in child.call('/api/simulation/events')['items']),'真实来源自动投递')
 portal=Client(urls['web']);login=portal.call('/auth/login',{'role':'member','username':parentEmail,'password':password});portal.csrf=login['csrf'];overview=portal.call('/api/v1/me/affiliate/overview');save('overview.json',overview);assert len(overview['direct_members'])==1,overview;stage('充值投注自动投递与达标')
 # 仅读取本次独立来源数据库中的已投递原事实生成清单，不写业务表，不伪造计奖结果。
 with sqlite3.connect(out/'source.db')as db:events=[json.loads(x[0])for x in db.execute("SELECT payload FROM facts WHERE kind IN ('DEPOSIT_SUCCEEDED','BET_SETTLED') AND state='DELIVERED'")]
 def ident(parts):return hashlib.sha256(json.dumps(parts,separators=(',',':'),ensure_ascii=False).encode()).hexdigest()
 parts=[]
 for e in events:
  fields=[e[k]for k in ['source_id','kind','business_id','external_user_id','currency','amount_minor','valid_bet_minor','venue_type_id','business_time']];parts.append(e['kind']+'\n'+e['business_id']+'\n'+ident(fields))
 mark={'source_id':'sandbox','business_day':day,'fact_count':len(events),'facts_digest':ident(sorted(parts))};raw=json.dumps(mark,separators=(',',':')).encode();sig=hmac.new(fact.encode(),('affiliate-watermark-v1\n'+tenant+'\ncommission\nsandbox\n').encode()+raw,hashlib.sha256).hexdigest()
 (out/'api/business-clock.txt').write_text(future)
 backend('/v1/affiliate/source-watermarks',mark,headers={'X-Affiliate-Signature':sig})
 (out/'api/business-clock.txt').write_text('REAL');(out/'worker/business-clock.txt').write_text(future)
 daily=wait(lambda:(lambda v:v if v.get('status')=='SETTLED'else None)(backend('/v1/affiliate/daily/'+day)),'自动worker日结',70);save('daily.json',daily)
 overview=portal.call('/api/v1/me/affiliate/progress');rewards=overview.get('rewards',[]);save('after-daily-overview.json',overview)
 commission=next(x for x in rewards if x['kind']=='COMMISSION');assert commission['amount_minor']=='40',commission
 portal.call('/api/v1/me/affiliate/claims',{'kind':'COMMISSION'},{'Idempotency-Key':uuid.uuid4().hex,'X-CSRF-Token':portal.csrf},want=409)
 backend('/v1/affiliate/commissions/'+commission['reward_id']+'/review',{'decision':'APPROVE','expected_revision':commission['revision'],'reason':'禁止申请人自审'},want=403)
 backend('/v1/affiliate/commissions/'+commission['reward_id']+'/review',{'decision':'APPROVE','expected_revision':commission['revision'],'reason':'全链路日结独立审核'},checker=True);stage('自动计佣与日结独立审核')
 claimKey=uuid.uuid4().hex;headers={'Idempotency-Key':claimKey,'X-CSRF-Token':portal.csrf};claim=portal.call('/api/v1/me/affiliate/claims',{'kind':'COMMISSION'},headers,want=202 if relay else 200);replay=portal.call('/api/v1/me/affiliate/claims',{'kind':'COMMISSION'},headers,want=202 if relay else 200);assert claim['claim_id']==replay['claim_id'];save('claim.json',claim)
 portal.call('/api/v1/me/affiliate/claims',{'kind':'COMMISSION'},{'Idempotency-Key':uuid.uuid4().hex,'X-CSRF-Token':portal.csrf},want=409)
 if relay:
  wait(lambda:relay.committed,'钱包实际提交后丢失响应')
  pending=portal.call('/api/v1/me/affiliate/progress');assert any(r['status']=='CLAIMING'for r in pending['rewards']),pending
  assert portal.call('/api/v1/me/wallets')['rows']==[],'不得同时计入内部钱包'
  deliveries=backend('/v1/affiliate/claim-deliveries');assert deliveries['rows'][0]['status']=='UNKNOWN',deliveries;save('pending-delivery.json',deliveries)
  stage('已入独立钱包但响应超时，保留领取处理中和原单')
 else:
  accounts=portal.call('/api/v1/me/wallets');transactions=portal.call('/api/v1/me/wallet-transactions');changes=portal.call('/api/v1/me/wallet-changes');save('wallet.json',{'accounts':accounts,'transactions':transactions,'changes':changes});assert accounts['rows'][0]['balance_minor']=='40',accounts;stage('会员领取、重复请求防重及钱包到账')
 child.call('/api/simulation/events',{'business_id':tenant+'-cancel','kind':'BET_CANCELLED','original_business_id':tenant+'-bet','amount':'100.00','valid_bet':'100.00','reason':'全链路已领佣金投注撤单'},want=202)
 if relay:
  wait(lambda:any(x['kind']=='BET_CANCELLED' and x['attempts']>0 for x in child.call('/api/simulation/events')['items']),'未知结果时冲正已尝试')
  assert not any(x['kind']=='BET_CANCELLED' and x['state']=='DELIVERED'for x in child.call('/api/simulation/events')['items']),'未确认钱包原单前不能冲正资金'
  relay.query_enabled=True
  wait(lambda:any(r['reward_id']==commission['reward_id'] and r['status']=='CLAIMED'for r in portal.call('/api/v1/me/affiliate/progress')['rewards']),'按原单查询确认到账')
  assert relay.queries>0 and relay.posts==1,'超时后必须查询，不能重复提交'
  relay.callback_enabled=True
  wait(lambda:relay.callback_success>0,'钱包自动回执失败重试恢复')
  callbackRaw,callbackSignature=relay.callbacks[-1]
  def callback(raw,signature,want):
   req=urllib.request.Request(urls['api']+'/v1/payment-callbacks/affiliate-wallet/'+walletClient,data=raw,headers={'X-Payment-Signature':signature,'Content-Type':'application/json'})
   try:r=urllib.request.urlopen(req,timeout=5)
   except urllib.error.HTTPError as e:r=e
   r.read();assert r.status==want,('回执状态',r.status,want)
  callback(callbackRaw,callbackSignature,204);callback(callbackRaw,callbackSignature,204)
  callback(callbackRaw,'0'*64,403)
  wrong=json.loads(callbackRaw);wrong['minor_units']='41';wrongRaw=json.dumps(wrong,separators=(',',':')).encode();callback(wrongRaw,hmac.new(callbackKey.encode(),wrongRaw,hashlib.sha256).hexdigest(),409)
  wrong['site_id']='other-site';wrongRaw=json.dumps(wrong,separators=(',',':')).encode();callback(wrongRaw,hmac.new(callbackKey.encode(),wrongRaw,hashlib.sha256).hexdigest(),400)
  stage('原单查询恢复、自动回执重试、重复回执和错站错金额拒绝')
 wait(lambda:len(child.call('/api/simulation/events')['items'])==3 and all(x['state']=='DELIVERED'for x in child.call('/api/simulation/events')['items']),'撤单自动重试并完成冲正',65)
 reversals=backend('/v1/affiliate/reversals');save('reversals.json',reversals)
 if relay:
  def walletGet(kind):
   path='/v1/wallet/'+kind;stamp=str(int(time.time()));signature=hmac.new(walletKey.encode(),'\n'.join(['wallet-sandbox-v1',walletClient,walletSite,stamp,'GET',path,'']).encode(),hashlib.sha256).hexdigest()
   return Client(urls['wallet']).call(path,headers={'X-Wallet-Client':walletClient,'X-Wallet-Site':walletSite,'X-Wallet-Time':stamp,'X-Wallet-Signature':signature})
  balances=walletGet('balances')['items'];entries=walletGet('ledger')['items'];assert len(entries)==1 and entries[0]['amount']=='40',entries;assert balances[0]['amount']=='40',balances
  own=portal.call('/api/v1/me/affiliate/claim-deliveries');assert len(own['rows'])==1 and own['rows'][0]['claim_id']==claim['claim_id'];assert own['rows'][0].get('callback_id'),'查询确认后仍须保存实际回执编号'
  childPortal=Client(urls['web']);childPortal.call('/auth/login',{'role':'member','username':childEmail,'password':password});assert childPortal.call('/api/v1/me/affiliate/claim-deliveries')['rows']==[],'会员不能读取推荐人的领取原单'
  portal.call('/api/v1/affiliate/claim-deliveries',want=403)
  portal.call('/api/v1/payment-callbacks/affiliate-wallet/'+walletClient,{},headers={'X-CSRF-Token':portal.csrf},want=404)
  deliveries=backend('/v1/affiliate/claim-deliveries');assert deliveries['rows'][0]['status']=='SUCCEEDED';save('external-wallet.json',{'balances':balances,'ledger':entries,'deliveries':deliveries,'relay_posts':relay.posts,'relay_queries':relay.queries,'callback_delivered':relay.callback_success,'callback_attempts':len(relay.callbacks)})
  assert portal.call('/api/v1/me/wallets')['rows']==[],'独立领取不能重复入内部钱包'
 else:
  walletAfter=portal.call('/api/v1/me/wallets');assert walletAfter==accounts,'冲正不能擅自扣已到账钱包'
  assert len(transactions['rows'])==1 and int(transactions['rows'][0]['amount_minor'])==40,transactions
 debt=sum(int(r['debt_added'])for r in reversals['items']);assert debt==40,reversals;stage('冲正原冻结方案重算、已领40分形成追偿')
 report={'result':'PASS','tenant':tenant,'wallet_mode':'external_wallet_sandbox' if relay else 'commission_internal_wallet','currency':'USD','commission_minor':'40','wallet_balance_minor':'40','recovery_debt_minor':str(debt),'claim_id':claim['claim_id'],'reward_id':commission['reward_id'],'source_events':[e['event_id']for e in events],'request_ids':[e['request_id']for e in evidence if e.get('request_id')],'checks':['审核前领取拒绝','申请人不能审核','重复及不同幂等键均不重复入账','邀请注册','来源自动投递','达标','自动worker日结','独立审核','领取幂等','独立钱包到账及超时原单查询' if relay else '内部钱包到账','原方案冲正','追偿及钱包流水核对'],'external_wallet_verified':bool(relay)};save('report.json',report);save('source-facts.json',events);save('progress.json',{'tenant':tenant,'steps':evidence});print(json.dumps({k:v for k,v in report.items() if k!='request_ids'},ensure_ascii=False))
finally:
 if relay:relay.close()
 for proc in reversed(processes):
  if proc.poll() is None:proc.send_signal(signal.SIGTERM)
 for proc in processes:
  try:proc.wait(timeout=15)
  except subprocess.TimeoutExpired:proc.kill();proc.wait()
