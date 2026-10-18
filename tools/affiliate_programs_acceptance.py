#!/usr/bin/env python3
# coding: utf-8
"""隔离本机验收：真实模拟会员投递，独立审批后并行计奖；不直接改写业务数据库。"""
import argparse,json,secrets,time,urllib.request,urllib.parse,http.cookiejar,uuid
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('--dir',required=True);args=p.parse_args();root=Path(args.dir);env=json.loads((root/'api-runtime.json').read_text());mcfg=json.loads((root/'member.json').read_text())
api='http://'+env['HTTP_ADDR'];public=mcfg['PublicURL'].rstrip('/')
for target in [api,public]:
 parsed=urllib.parse.urlparse(target)
 if parsed.scheme!='http' or parsed.hostname not in ['127.0.0.1','::1'] or parsed.username or parsed.password:
  raise SystemExit('拒绝非本机隔离环境')
if not env['LOCAL_TENANT'].startswith('program-preview-'):
 raise SystemExit('拒绝非本功能隔离租户')
def backend(path,data=None,approver=False):
 raw=json.dumps(data).encode() if data is not None else None
 headers={'Authorization':'Bearer '+env['LOCAL_APPROVER_TOKEN' if approver else 'LOCAL_OPERATOR_TOKEN'],'Content-Type':'application/json','Idempotency-Key':uuid.uuid4().hex}
 with urllib.request.urlopen(urllib.request.Request(api+path,data=raw,headers=headers),timeout=15) as r:return json.load(r)['data']
def approve(path,body):
 req=backend(path,body);backend('/v1/approvals/'+req['approval_id']+'/decisions',dict(decision='APPROVE',expected_revision=0,object_hash=req['object_hash'],reason='独立审核本地方案验收'),True)
def wait(fn,label):
 for _ in range(100):
  v=fn()
  if v:return v
  time.sleep(.15)
 raise AssertionError('等待超时：'+label)
condition={'deposit_minor':'100','valid_bet_minor':'100'}
cfg={'enabled':True,'currency':'USD','currency_scale':2,'timezone':'UTC','grade_period':'DAY','grade_condition':condition,'referral_condition':condition,'invitee_condition':condition,'invitee_reward_minor':'50','levels':[{'level':0,'qualified_members':0,'daily_valid_bet_minor':'0','rates_ppm':[2500,1500,500]}],'steps':[{'step':1,'additional_members':1,'reward_minor':'50'}],'venue_type_ids':['SPORTS','LIVE']}
if backend('/v1/affiliate/config')['revision']==0:approve('/v1/affiliate/config-requests',dict(config=cfg,expected_revision=0,reason='并行方案兼容配置'))
prefix='program-'+uuid.uuid4().hex[:8]
from datetime import datetime,timezone,timedelta
def publish(id,name,state,revision):
 body=dict(program_id=id,name=name,state=state,expected_revision=revision,effective_from=(datetime.now(timezone.utc)+timedelta(seconds=3)).isoformat(timespec='microseconds'),reason='并行方案生命周期验收',match=dict(sources=['sandbox'],event_types=[],member_ids=[],mode='STACK',group='',priority=0),config=cfg)
 backend('/v1/affiliate/programs/preview',body);approve('/v1/affiliate/programs/requests',body)
 wait(lambda:any(v['program_id']==id and v['state']==state and v['revision']==revision+1 for v in backend('/v1/affiliate/programs')['current']),state)
 return body
class Member:
 def __init__(self):self.opener=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
 def call(self,path,data=None):
  raw=json.dumps(data).encode() if data is not None else None
  with self.opener.open(urllib.request.Request(public+path,data=raw,headers={'Origin':public,'Content-Type':'application/json'}),timeout=10) as r:return json.load(r)
password=secrets.token_urlsafe(24);parent=Member();parent.call('/api/register',dict(email=prefix+'-parent@example.test',display_name='多方案演示推荐人',password=password));wait(lambda:parent.call('/api/me')['delivery_status']=='SUCCEEDED','推荐人投递');url=parent.call('/api/invitation',{})['invitation_url'];ic=urllib.parse.parse_qs(urllib.parse.urlparse(url).query)['ic'][0]
def child(number):
 m=Member();email=f'{prefix}-child{number}@example.test';m.call('/api/register',dict(email=email,display_name='多方案演示会员',password=password,invitation_code=ic));wait(lambda:m.call('/api/me')['delivery_status']=='SUCCEEDED','受邀注册');return m,email
member,email=child(1);aid=prefix+'-a';bid=prefix+'-b';publish(aid,'推荐活动 A','READY',0)
def facts(m,label):
 for suffix,kind,amount,valid,venue in [('deposit','DEPOSIT_SUCCEEDED','1.00','0',''),('bet','BET_SETTLED','100.00','100.00','SPORTS')]:m.call('/api/simulation/events',dict(business_id=prefix+'-'+label+'-'+suffix,kind=kind,amount=amount,valid_bet=valid,venue_type_id=venue))
 wait(lambda:all(r['state']=='DELIVERED' for r in m.call('/api/simulation/events')['items']),'充值投注自动投递')
facts(member,'ready');assert not any(r.get('program_id')==aid for r in backend('/v1/affiliate/rewards')['rows'])
publish(aid,'推荐活动 A','RUNNING',1);publish(bid,'推荐活动 B','RUNNING',0);facts(member,'running');rows=backend('/v1/affiliate/rewards')['rows'];assert all(any(r.get('program_id')==id and r['kind']=='INVITEE' for r in rows) for id in [aid,bid])
publish(aid,'推荐活动 A','STOPPED',2);member2,email2=child(2);facts(member2,'stopped');rows=backend('/v1/affiliate/rewards')['rows'];assert sum(r.get('program_id')==aid and r['kind']=='INVITEE' for r in rows)==1;assert sum(r.get('program_id')==bid and r['kind']=='INVITEE' for r in rows)==2
(root/'account.json').write_text(json.dumps(dict(email=email,password=password)));(root/'account.json').chmod(0o600)
result={'result':'PASS','prefix':prefix,'checks':['真实邀请注册与自动来源投递','未启动不计算','两个方案启动并行计奖','停止后不新增权益','停止后的版本与命中历史保留'],'admin':'http://127.0.0.1:19793/','member':public};(root/'acceptance.json').write_text(json.dumps(result,ensure_ascii=False,indent=2));print(json.dumps(result,ensure_ascii=False))
