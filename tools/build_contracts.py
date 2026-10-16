"""生成 V1 基础契约；完整方案契约完成前禁止发布。"""
import json
from pathlib import Path
OUT=Path('contracts/v1')
def obj(props,required=None):
    return {'type':'object','properties':props,'required':list(props) if required is None else required,'additionalProperties':False}
def enum(*values): return {'type':'string','enum':list(values)}
def write(name,data):
    (OUT/name).write_text(json.dumps(data,ensure_ascii=False,indent=2)+'\n')
identifier={'type':'string','minLength':1,'maxLength':128}
decimal={'type':'string','pattern':r'^[0-9]{1,18}(\.[0-9]{1,8})?$'}
minor={'type':'string','pattern':r'^[0-9]+$'}
currency={'type':'string','minLength':1,'maxLength':128}
time={'type':'string','format':'date-time','pattern':r'^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.[0-9]{1,6})?Z$'}
event_types=['FEE_CONFIRMED','TRADE_CLOSED','ORDER_CONFIRMED']
def schema(name,body):
    return {'$schema':'https://json-schema.org/draft/2020-12/schema','$id':f'urn:excommission:v1:{name}',**body}
money=schema('money',obj({'currency':currency,'minor_units':minor}))
event=schema('event',obj({'schema_version':enum('1'),'source_id':identifier,'event_id':identifier,'business_id':identifier,'event_type':enum(*event_types),'business_time':time,'external_user_id':identifier,'product_code':identifier,'scene':identifier,'source_revision':enum('1'),'payload':obj({'basis_amount':decimal,'basis_currency':currency,'quantity':decimal,'quantity_unit':identifier,'confirmed':{'const':True}},['basis_amount','basis_currency'])}))
event['properties']['payload']['dependentRequired']={'quantity':['quantity_unit'],'quantity_unit':['quantity']}
component=schema('component',obj({'component_type':enum('Calculator','IdentityProvider','SourceAdapter','PaymentProvider','NotificationProvider','AuditSink'),'component_id':identifier,'implementation_version':identifier,'contract_version':enum('1'),'capabilities':{'type':'array','items':identifier,'uniqueItems':True},'config_schema_ref':identifier,'credential_ref':{'type':['string','null']},'enabled':{'type':'boolean'},'validation_status':enum('PENDING','VALID','INVALID')}))
write('money.schema.json',money);write('event.schema.json',event);write('component.schema.json',component)
errors='VALIDATION_FAILED UNSUPPORTED_CAPABILITY UNIT_MISMATCH IDEMPOTENCY_CONFLICT REVISION_CONFLICT STALE_APPROVAL NO_EFFECTIVE_PLAN_VERSION RELATION_HISTORY_MISSING RATE_MISSING UNKNOWN_FACT BUDGET_EXCEEDED BASIS_CURRENCY_MISMATCH PAYMENT_UNKNOWN PAYMENT_FINAL_CONFLICT DEPENDENCY_UNAVAILABLE PERMISSION_DENIED'.split()
write('errors.json',{'standard_version':'1','codes':errors})
# 此处只声明已经具备输入契约的事件接口，不以空对象冒充其他接口冻结完成。
write('openapi.json',{'openapi':'3.1.0','info':{'title':'ExCommission V1 基础契约（未冻结）','version':'0.1.0'},'security':[{'bearerAuth':[]}],'paths':{'/v1/events':{'post':{'operationId':'acceptEvent','parameters':[{'name':'Idempotency-Key','in':'header','required':True,'schema':{'type':'string','pattern':'^[ -~]{1,128}$'}}],'requestBody':{'required':True,'content':{'application/json':{'schema':{'$ref':'#/components/schemas/BusinessEvent'}}}},'responses':{str(code):{'description':description} for code,description in [(202,'接收成功；不代表已经计佣或付款'),(400,'输入非法'),(401,'未认证'),(403,'无权'),(409,'幂等冲突'),(422,'业务条件不满足'),(429,'限流'),(503,'瞬时不可用')]}}}},'components':{'securitySchemes':{'bearerAuth':{'type':'http','scheme':'bearer'}},'schemas':{'BusinessEvent':{k:v for k,v in event.items() if k not in ('$schema','$id')}}}})
# 严格配置契约；跨字段和历史约束通过领域校验器执行。
def arr(items,minimum=0): return {'type':'array','items':items,'minItems':minimum}
def nullable(value): return {'anyOf':[value,{'type':'null'}]}
boolean={'type':'boolean'}
revision={'type':'integer','minimum':0}
ppm={'type':'string','pattern':r'^(1000000|[0-9]{1,6})$'}
metric=enum('basis_amount','quantity','product_code','scene','confirmed','business_time')
leaf=obj({'metric':metric,'operator':enum('EQ','NE','GT','GTE','LT','LTE','IN'),'value':{'oneOf':[{'type':'string'},boolean,arr({'type':'string'},1)]},'unit':identifier})
condition={'oneOf':[leaf,obj({'logic':enum('AND','OR'),'children':arr({'$ref':'#/$defs/condition'},1)}),obj({'logic':enum('NOT'),'children':arr({'$ref':'#/$defs/condition'},1)})]}
condition['oneOf'][2]['properties']['children']['maxItems']=1
true_condition=obj({'logic':enum('AND'),'children':{'type':'array','maxItems':0}})
tier=obj({'lower':decimal,'upper':nullable(decimal),'rate_ppm':ppm})
parameters={
 'PERCENT':obj({'rate_ppm':ppm}),
 'FIXED':obj({'amount_minor':minor,'reward_currency':currency}),
 'UNIT_PRICE':obj({'unit_price':decimal,'quantity_unit':identifier,'reward_currency':currency}),
 'INDEPENDENT':obj({'rates':arr(obj({'product_code':identifier,'grade_id':identifier,'depth':{'type':'integer','minimum':1,'maximum':64},'rate_ppm':ppm}),1)}),
 'DIFFERENTIAL':obj({'rates':arr(obj({'product_code':identifier,'grade_id':identifier,'rate_ppm':ppm}),1)}),
 'MARGINAL':obj({'tiers':arr(tier,1)}),
 'HIGHEST_RATE':obj({'tiers':arr(tier,1)}),
 'WEIGHTED':obj({'pool_minor':minor,'reward_currency':currency,'beneficiaries':arr(obj({'beneficiary_id':identifier,'weight':{'type':'string','pattern':'^[1-9][0-9]*$'}}),1)})}
variants=[]
for algorithm,params in parameters.items():
    variants.append(obj({'id':identifier,'algorithm_id':enum(algorithm),'algorithm_version':enum('1'),'recipient':enum('EXPLICIT') if algorithm=='WEIGHTED' else enum('DIRECT','ANCESTORS'),'condition':{'oneOf':[{'$ref':'#/$defs/condition'},true_condition]},'parameters':params,'funding_limit_ppm':{'type':'null'} if algorithm in ('FIXED','UNIT_PRICE','WEIGHTED') else ppm,'per_beneficiary_cap_minor':nullable(minor),'per_event_total_cap_minor':nullable(minor),'frequency':enum('ONCE_PER_BUSINESS_RULE')}))
plan=schema('plan',obj({'plan_id':identifier,'name':identifier,'revision':revision,'standard_version':enum('1'),'route':obj({'product_code':identifier,'event_type':enum(*event_types),'scene':identifier}),'currency':currency,'calendar_version':identifier,'agent_policy':obj({'max_depth':{'type':'integer','minimum':1,'maximum':64,'default':3},'self_reward':{'const':False},'earn_while_frozen':boolean,'require_contract':boolean,'grade_window':enum('PREVIOUS_CALENDAR_MONTH'),'grade_evaluation':enum('DAILY'),'eligibility_required':{'const':True}}),'grade_definitions':arr(obj({'id':identifier,'rank':{'type':'integer','minimum':0},'conditions':arr(obj({'metric':enum('PERSONAL_NET_BASIS','TEAM_NET_BASIS','ACTIVE_DIRECT_COUNT'),'operator':enum('EQ','NE','GT','GTE','LT','LTE'),'value':decimal,'unit':identifier}))}),1),'rules':arr({'oneOf':variants},1),'event_budget_minor':nullable(minor),'settlement_policy':obj({'period':enum('DAILY'),'minimum_payout_minor':minor,'max_payment_minor':nullable(minor),'late_policy':enum('NEXT_OPEN_PERIOD'),'channel_fee_bearer':enum('PLATFORM')})}))
plan['$defs']={'condition':condition}
plan['x-domain-validation']=['条件深度<=8且叶子<=100；指标类型及单位匹配','等级rank唯一且存在rank0空条件；费率可达维度完整','阶梯从0连续且费率非递减，末档upper=null','固定、单价、加权要求显式方案预算','各规则预算和方案预算；禁止部分入账','范围、资格、合同、关系必须按业务时点冻结','发布摘要绑定试算与独立审批；版本区间不可重叠']
write('plan.schema.json',plan)
refund=schema('refund',obj({'source_id':identifier,'refund_id':identifier,'original_business_id':identifier,'original_event_type':enum(*event_types),'refund_basis_total':decimal,'currency':currency,'business_time':time,'reason':identifier,'refund_quantity_total':decimal,'quantity_unit':identifier},['source_id','refund_id','original_business_id','original_event_type','refund_basis_total','currency','business_time','reason']))
refund['dependentRequired']={'refund_quantity_total':['quantity_unit'],'quantity_unit':['refund_quantity_total']}
write('refund.schema.json',refund)
# 开放API登记：所有修改均要求幂等键；已有对象变更显式修订号。
api=json.loads((OUT/'openapi.json').read_text())
api['info']={'title':'ExCommission V1 契约','version':'1.0.0'}
components=api['components']['schemas']
components.update({'Money':{k:v for k,v in money.items() if not k.startswith('$')},'Refund':{k:v for k,v in refund.items() if k not in ('$schema','$id')}})
# 使用内部重定位引用，避免OpenAPI解析器误把文档根当作schema根。
plan_api=json.loads(json.dumps(plan).replace('#/$defs/condition','#/components/schemas/Condition'))
components['Condition']=json.loads(json.dumps(condition).replace('#/$defs/condition','#/components/schemas/Condition'))
components['PlanDraft']={k:v for k,v in plan_api.items() if k not in ('$schema','$id','$defs')}
ref=lambda name:{'$ref':'#/components/schemas/'+name}
error=obj({'error':obj({'code':{'type':'string'},'message':{'type':'string'},'retryable':boolean,'resource_ref':nullable(identifier),'details':{'type':'object'}}),'request_id':identifier})
components['Error']=error
principal_errors={str(c):{'description':label,'content':{'application/json':{'schema':ref('Error')}}} for c,label in [(400,'字段非法'),(401,'未认证'),(403,'无权'),(404,'作用域内不存在'),(409,'幂等、版本或状态冲突'),(422,'业务约束不满足'),(429,'限流'),(503,'瞬时不可用')]}
idem={'name':'Idempotency-Key','in':'header','required':True,'schema':{'type':'string','pattern':'^[ -~]{1,128}$'}}
rev={'expected_revision':revision};reason={'reason':identifier}
def endpoint(path,method,body=None,code=200,query=None):
    operation={'operationId':method+'_'+re.sub('[^a-zA-Z0-9]+','_',path).strip('_'),'responses':dict(principal_errors)}
    operation['parameters']=[{'name':name,'in':'path','required':True,'schema':identifier} for name in re.findall(r'{(.*?)}',path)]
    if method!='get': operation['parameters'].append(idem)
    for name,definition,required in query or []:
        operation['parameters'].append({'name':name,'in':'query','required':required,'schema':definition})
    if body is not None: operation['requestBody']={'required':True,'content':{'application/json':{'schema':body}}}
    # 不稳定的资源响应只冻结外层信封，领域资源字段由相应实现任务补齐。
    operation['responses'][str(code)]={'description':'成功；异步接收不代表业务执行成功','content':{'application/json':{'schema':obj({'data':{'type':'object'},'request_id':identifier})}}}
    api['paths'].setdefault(path,{})[method]=operation
import re
endpoint('/v1/events','post',ref('BusinessEvent'),202)
for path in ['/v1/events/{id}','/v1/calculation-tasks/{id}','/v1/calculation-tasks/{id}/trace','/v1/user-sync-jobs/{id}','/v1/approvals/{id}','/v1/payments/{id}','/v1/plans/{id}/versions','/v1/plans/{id}/versions/{version_id}','/v1/report-exports/{id}']:
    endpoint(path,'get')
endpoint('/v1/user-sync-jobs','post',obj({'source_id':identifier,'mapping_version_id':identifier,'cursor':nullable(identifier)}),202)
endpoint('/v1/agents','post',obj({'user_id':identifier,'display_name':identifier}))
endpoint('/v1/agents/{id}/ancestors','get',query=[('at',time,True),('limit',{'type':'integer','minimum':1,'maximum':100,'default':64},False),('cursor',identifier,False)])
endpoint('/v1/relationship-change-requests','post',obj({'agent_id':identifier,'new_parent_id':nullable(identifier),**rev,**reason}))
endpoint('/v1/grade-change-requests','post',obj({'agent_id':identifier,'plan_id':identifier,'grade_id':identifier,'eligibility':enum('ELIGIBLE','INELIGIBLE'),**rev,**reason}))
endpoint('/v1/grade-evaluations','post',obj({'plan_id':identifier,'calendar_period':identifier,'cutoff':time,'expected_previous_evaluation':nullable(identifier)}))
endpoint('/v1/plans','post',ref('PlanDraft'))
endpoint('/v1/plans/{id}/draft','put',obj({'draft':ref('PlanDraft'),**rev}))
endpoint('/v1/plans/{id}/draft','get')
endpoint('/v1/simulations','post',{'oneOf':[obj({key:identifier,'business_facts':{'type':'object'},'ancestors':arr({'type':'object'}),'qualification_facts':{'type':'object'}}) for key in ('draft_hash','plan_version_id')]})
endpoint('/v1/plans/{id}/publication-requests','post',obj({**rev,'draft_hash':identifier,'simulation_id':identifier,'effective_from':time,**reason}))
endpoint('/v1/approvals/{id}/execution-retries','post',obj({**rev,'object_hash':identifier,**reason,'evidence_ref':identifier}))
endpoint('/v1/approvals/{id}/cancellation','post',obj({**rev,'object_hash':identifier,**reason}))
endpoint('/v1/approvals/{id}/decisions','post',obj({'decision':enum('APPROVE','REJECT'),**rev,'object_hash':identifier,**reason}))
endpoint('/v1/plans/{id}/rollback-requests','post',obj({'target_version_id':identifier,'effective_from':time,**reason,**rev}))
endpoint('/v1/statement-runs','post',obj({'period':identifier,'calendar_version':identifier,'agent_ids':{'type':'array','items':identifier,'uniqueItems':True}},['period','calendar_version']),202)
endpoint('/v1/statements/{id}','get')
endpoint('/v1/statements/{id}/submission','post',obj({**rev,'content_hash':identifier}),202)
endpoint('/v1/payments','post',obj({'statement_id':identifier,'amount':ref('Money'),'channel_id':identifier,'expected_statement_revision':revision}),202)
endpoint('/v1/payments/{id}/verification','post',obj({}))
endpoint('/v1/payment-callbacks/{channel_id}','post',{'description':'渠道原始字节，由适配器验签，不使用公共身份或幂等头；渠道回调ID去重。'})
callback=api['paths']['/v1/payment-callbacks/{channel_id}']['post'];callback['security']=[];callback['parameters']=[p for p in callback['parameters'] if p['in']=='path']
callback['parameters'].append({'name':'X-Payment-Signature','in':'header','required':True,'schema':{'type':'string','pattern':'^[0-9a-f]{64}$'}})
endpoint('/v1/reconciliation-runs','post',obj({'channel_id':identifier,'date_range':obj({'from':time,'to':time}),'statement_file_ref':identifier}))
endpoint('/v1/refunds','post',ref('Refund'),202)
endpoint('/v1/refunds/{id}','get')
endpoint('/v1/debts/{id}','get')
endpoint('/v1/entitlements/{id}','get')
endpoint('/v1/calculation-tasks/{id}/adjustments','get')
api['paths']['/v1/refunds']['post']['parameters'].append({'name':'X-Source-Signature','in':'header','required':True,'schema':{'type':'string','pattern':'^[0-9a-f]{64}$'}})
endpoint('/v1/recovery-requests','post',obj({'debt_id':identifier,'method':enum('FUTURE_OFFSET','CASH_CONFIRMED'),'amount':ref('Money'),'evidence_ref':identifier,**reason,**rev}),202)
endpoint('/v1/adjustment-requests','post',obj({'original_task_id':identifier,'corrected_facts':{'type':'object'},**reason,'evidence_ref':identifier,**rev}),202)
endpoint('/v1/risk-holds','post',obj({'entitlement_id':identifier,'reason_code':identifier,'evidence_ref':identifier}),201)
endpoint('/v1/risk-holds/{id}','get')
endpoint('/v1/risk-holds/{id}/appeals','post',obj({'reason':identifier,'evidence_ref':identifier}),201)
endpoint('/v1/agents/{id}','get')
endpoint('/v1/agent-status-change-requests','post',obj({'agent_id':identifier,'status':enum('ACTIVE','FROZEN','DISABLED','CLOSED'),**rev,**reason,'evidence_ref':identifier}),202)
endpoint('/v1/risk-holds/{id}/release-requests','post',obj({**rev,**reason,'evidence_ref':identifier}),202)
endpoint('/v1/permission-change-requests','post',obj({'subject_id':identifier,'grant':arr(identifier),'revoke':arr(identifier),**rev,**reason,'evidence_ref':identifier}),202)
endpoint('/v1/identity/subjects/{id}/permissions','get')
endpoint('/v1/security-events/{id}/handling-records','post',obj({**rev,'status':enum('OPEN','ACKNOWLEDGED','RESOLVED'),**reason}))
endpoint('/v1/audit-logs','get',query=[('type',enum('OPERATION','SECURITY'),False),('time_range',identifier,False),('actor',identifier,False),('object_ref',identifier,False)])
currency_entry=obj({'code':currency,'exponent':{'type':'integer','minimum':0,'maximum':8},'display_format':identifier,'payment_channels':arr(identifier)})
setting_configuration={'oneOf':[obj({'version_id':identifier,'effective_from':time,'currencies':arr(currency_entry)}),obj({'version_id':identifier,'effective_from':time,'timezone':enum('UTC'),'period':enum('DAILY')}),obj({'component_type':enum('SOURCE','PAYMENT','NOTIFICATION'),'component_id':identifier,'enabled':boolean})]}
endpoint('/v1/platform-setting-requests','post',obj({'kind':enum('CURRENCY_CATALOG','CALENDAR','COMPONENT_STATE'),'configuration':setting_configuration,**rev,**reason}),202)
endpoint('/v1/platform/catalogs','get')
endpoint('/v1/platform/components','get')
endpoint('/v1/notifications','get')
endpoint('/v1/reports/{kind}','get',query=[(name,time if name in ('from','to') else identifier,False) for name in ('from','to','plan_id','version_id','agent_id','currency')])
endpoint('/v1/report-exports','post',obj({'kind':enum('earnings','finance','cost'),'filters':obj({name:time if name in ('from','to') else identifier for name in ('from','to','plan_id','version_id','agent_id','currency')},[]),'format':enum('CSV')}),202)
endpoint('/v1/report-exports/{id}/download','get')
api['paths']['/v1/report-exports/{id}/download']['get']['responses']['410']={'description':'快照过期，不重新生成原文件'}
api['x-contract-status']='INPUT_DRAFT_OUTPUT_PENDING'
api['x-unresolved']=['范围配置字段在业务标准中有语义但PlanDraft未给定字段，发布门禁前需定稿','快照、事实类型及全部成功响应字段仍须完成严格契约','公开契约冻结前不得声称任务01完成']
write('openapi.json',api)
# 已获用户确认的范围契约补全；不改变既有业务语义。
plan['properties']['scope']={'oneOf':[obj({'type':enum('ALL_ACTIVE_AGENTS'),'agent_ids':{'type':'array','maxItems':0}}),obj({'type':enum('EXPLICIT_AGENT_IDS'),'agent_ids':{'type':'array','items':identifier,'minItems':1,'uniqueItems':True}})]}
plan['required'].append('scope')
write('plan.schema.json',plan)
plan_api=json.loads(json.dumps(plan).replace('#/$defs/condition','#/components/schemas/Condition'))
components['PlanDraft']={k:v for k,v in plan_api.items() if k not in ('$schema','$id','$defs')}
facts=obj({'basis_amount':decimal,'basis_currency':currency,'quantity':decimal,'quantity_unit':identifier,'product_code':identifier,'scene':identifier,'confirmed':boolean,'business_time':time},[])
actor=obj({'agent_id':identifier,'depth':{'type':'integer','minimum':1,'maximum':64},'grade_id':identifier,'eligible':boolean,'status':enum('ACTIVE','FROZEN','DISABLED','CLOSED')})
# 资格、合同与水位是按键索引的事实目录，键为领域对象ID，值是受约束的事实。
qualification={'type':'object','additionalProperties':obj({'eligibility':enum('ELIGIBLE','INELIGIBLE'),'effective_from':time,'effective_to':nullable(time)})}
contracts={'type':'object','additionalProperties':obj({'contract_id':identifier,'plan_id':identifier,'effective_from':time,'effective_to':nullable(time)})}
snapshot=schema('snapshot',obj({'snapshot_id':identifier,'standard_version':enum('1'),'plan_id':identifier,'plan_version_id':identifier,'config_hash':identifier,'business_time':time,'currency_catalog_version':identifier,'calendar_version':identifier,'facts':facts,'ancestors':arr(actor),'qualification_facts':qualification,'contract_facts':contracts,'algorithm_versions':{'type':'object','additionalProperties':enum('1')}}))
write('snapshot.schema.json',snapshot)
item=obj({'rule_id':identifier,'beneficiary_id':identifier,'depth':{'type':'integer','minimum':1,'maximum':64},'currency':currency,'minor_units':minor,'algorithm_id':enum(*parameters),'algorithm_version':enum('1')})
result=obj({'task_id':identifier,'snapshot_id':identifier,'status':enum('QUEUED','RUNNING','SUCCEEDED','NO_MATCH','SUSPENDED','RETRY_WAIT','DEAD'),'items':arr(item),'reason_codes':arr(identifier),'trace':arr({'type':'object'})})
components['CalculationResult']=result
components['Snapshot']={k:v for k,v in snapshot.items() if k not in ('$schema','$id')}
components['Facts']=facts
components['ActorRef']=actor
# 正式模拟输入与计算使用相同事实定义。
sim=api['paths']['/v1/simulations']['post']['requestBody']['content']['application/json']['schema']
for variant in sim['oneOf']:
    variant['properties'].update({'business_facts':ref('Facts'),'ancestors':arr(ref('ActorRef')),'qualification_facts':qualification})
api['paths']['/v1/adjustment-requests']['post']['requestBody']['content']['application/json']['schema']['properties']['corrected_facts']=ref('Facts')
# 资源响应允许未来增加可选字段；请求与配置保持拒绝未知字段。
def response_obj(props,required=None):
    value=obj(props,required);value['additionalProperties']=True;return value
id_data=response_obj({'resource_id':identifier},[])
responses={
 '/v1/events':response_obj({'event_id':identifier,'task_id':identifier,'acceptance_status':enum('ACCEPTED','REJECTED','WAITING_ORIGINAL')}),
 '/v1/events/{id}':response_obj({'event_id':identifier,'task_id':identifier,'acceptance_status':enum('ACCEPTED','REJECTED','WAITING_ORIGINAL')}),
 '/v1/calculation-tasks/{id}':ref('CalculationResult'),
 '/v1/calculation-tasks/{id}/trace':response_obj({'task_id':identifier,'trace':arr({'type':'object'})}),
 '/v1/user-sync-jobs':response_obj({'job_id':identifier}),
 '/v1/user-sync-jobs/{id}':response_obj({'job_id':identifier,'watermark':nullable(identifier),'success_count':{'type':'integer','minimum':0},'failure_count':{'type':'integer','minimum':0},'error_refs':arr(identifier)}),
 '/v1/agents':response_obj({'agent_id':identifier,'revision':revision,'status':enum('ACTIVE')}),
 '/v1/agents/{id}/ancestors':response_obj({'items':arr(response_obj({'agent_id':identifier,'depth':{'type':'integer','minimum':1},'effective_from':time,'effective_to':nullable(time)})),'next_cursor':nullable(identifier)}),
 '/v1/simulations':response_obj({'simulation_id':identifier,'snapshot_hash':identifier,'result':ref('CalculationResult')}),
 '/v1/approvals/{id}':response_obj({'approval_id':identifier,'approval_status':enum('PENDING','APPROVED','REJECTED','CANCELLED','STALE'),'execution_status':enum('PENDING','SUCCEEDED','FAILED')}),
 '/v1/approvals/{id}/decisions':response_obj({'approval_status':enum('APPROVED','REJECTED','STALE'),'execution_status':enum('PENDING','SUCCEEDED','FAILED')}),
 '/v1/payments':response_obj({'payment_id':identifier,'merchant_payment_id':identifier,'status':enum('CREATED','SUBMITTED','SUCCEEDED','FAILED_FINAL','UNKNOWN')}),
 '/v1/payments/{id}':response_obj({'payment_id':identifier,'merchant_payment_id':identifier,'status':enum('CREATED','SUBMITTED','SUCCEEDED','FAILED_FINAL','UNKNOWN'),'amount':ref('Money'),'receipt':{'type':['object','null']},'reconciliation_status':identifier}),
 '/v1/refunds':response_obj({'refund_task_id':identifier,'status':identifier}),
 '/v1/risk-holds':response_obj({'risk_id':identifier}),
 '/v1/report-exports':response_obj({'export_id':identifier}),
 '/v1/report-exports/{id}':response_obj({'export_id':identifier,'status':identifier}),
 '/v1/reports/{kind}':response_obj({'rows':arr({'type':'object'}),'as_of':time,'source_watermark':identifier,'currency_catalog_version':identifier}),
 '/v1/audit-logs':response_obj({'items':arr({'type':'object'}),'next_cursor':nullable(identifier)})}
for path,ops in api['paths'].items():
    for method,op in ops.items():
        for selector in ('X-Tenant-ID','X-App-ID'):
            if path!='/v1/payment-callbacks/{channel_id}': op['parameters'].append({'name':selector,'in':'header','required':False,'schema':identifier,'description':'仅选择已授权作用域，不作为可信身份来源'})
        op['parameters'].append({'name':'X-Request-ID','in':'header','required':False,'schema':identifier})
        data=responses.get(path)
        if data is None:
            if path in ('/v1/plans','/v1/plans/{id}/draft'): data=ref('PlanDraft')
            elif path.endswith('/versions'): data=response_obj({'items':arr(response_obj({'version_id':identifier,'effective_from':time,'effective_to':nullable(time)})),'next_cursor':nullable(identifier)})
            elif path.endswith('/versions/{version_id}'): data=response_obj({'version_id':identifier,'draft':ref('PlanDraft'),'effective_from':time,'effective_to':nullable(time)})
            elif path in ('/v1/statement-runs','/v1/reconciliation-runs'): data=response_obj({'run_id':identifier})
            elif path=='/v1/grade-evaluations': data=response_obj({'evaluation_id':identifier,'source_watermark':identifier,'effective_from':time})
            elif path in ('/v1/payments/{id}/verification','/v1/payment-callbacks/{channel_id}'): data=response_obj({'payment_id':identifier,'status':identifier})
            elif path=='/v1/security-events/{id}/handling-records': data=response_obj({'record_id':identifier})
            else: data=response_obj({'approval_id':identifier})
        for code,response in op['responses'].items():
            if code.startswith('2'): response['content']['application/json']['schema']=obj({'data':data,'request_id':identifier})
        if path=='/v1/report-exports/{id}/download':
            op['responses']['200']={'description':'已授权导出快照；重新鉴权并审计','content':{'text/csv':{'schema':{'type':'string','format':'binary'}}}}
        if method=='get' and path.endswith(('/versions','/audit-logs')):
            op['parameters'] += [{'name':'limit','in':'query','schema':{'type':'integer','minimum':1,'maximum':100,'default':20}},{'name':'cursor','in':'query','schema':identifier}]
api['x-contract-status']='V1_STRUCTURAL_BASELINE'
api.pop('x-unresolved',None)
write('openapi.json',api)
# 方案资源读取与草稿读取均提供，避免仅提供草稿子资源。
endpoint('/v1/plans/{id}','get')
api['paths']['/v1/plans/{id}']['get']['responses']['200']['content']['application/json']['schema']=obj({'data':ref('PlanDraft'),'request_id':identifier})
write('openapi.json',api)
# 方案资源响应在配置字段之外提供摘要与状态；命令配置仍拒绝未知字段。
plan_response=json.loads(json.dumps(components['PlanDraft']))
plan_response['additionalProperties']=True
plan_response['properties'].update({'draft_hash':identifier,'status':enum('DRAFT','PENDING_APPROVAL','APPROVED','PUBLISHED','RETIRED')})
components['PlanDocument']=plan_response
for path in ('/v1/plans','/v1/plans/{id}','/v1/plans/{id}/draft'):
    for operation in api['paths'][path].values():
        for code,response in operation['responses'].items():
            if code.startswith('2'):
                response['content']['application/json']['schema']=obj({'data':ref('PlanDocument'),'request_id':identifier})
write('openapi.json',api)
# C02：经用户确认的条件类型、单位与比较符。
condition['oneOf'][0]={'oneOf':[
    obj({'metric':enum('basis_amount','quantity'),'operator':enum('EQ','NE','GT','GTE','LT','LTE'),'value':decimal,'unit':identifier}),
    obj({'metric':enum('confirmed'),'operator':enum('EQ','NE'),'value':boolean,'unit':enum('BOOLEAN')}),
    obj({'metric':enum('product_code','scene'),'operator':enum('EQ','NE'),'value':identifier,'unit':enum('ENUM')}),
    obj({'metric':enum('product_code','scene'),'operator':enum('IN'),'value':arr(identifier,1),'unit':enum('ENUM')}),
    obj({'metric':enum('business_time'),'operator':enum('EQ','NE','GT','GTE','LT','LTE'),'value':time,'unit':enum('UTC')})]}
plan['$defs']['condition']=condition
write('plan.schema.json',plan)
components['Condition']=json.loads(json.dumps(condition).replace('#/$defs/condition','#/components/schemas/Condition'))
write('openapi.json',api)
# EXPLICIT 候选不伪装祖先；资格快照允许补充状态，非祖先深度为0。
for definition in (qualification,):
    definition['additionalProperties']['properties'].update({'status':enum('ACTIVE','FROZEN','DISABLED','CLOSED'),'grade_id':identifier})
snapshot['properties']['qualification_facts']=qualification
snapshot['properties']['recipient_facts']={'type':'object','additionalProperties':obj({'agent_id':identifier,'eligible':boolean,'status':enum('ACTIVE','FROZEN','DISABLED','CLOSED'),'depth':{'type':'integer','minimum':0,'maximum':64}},['agent_id','eligible','status','depth'])}
write('snapshot.schema.json',snapshot)
item['properties']['depth']['minimum']=0
components['Snapshot']={k:v for k,v in snapshot.items() if k not in ('$schema','$id')}
for variant in api['paths']['/v1/simulations']['post']['requestBody']['content']['application/json']['schema']['oneOf']:
    variant['properties']['qualification_facts']=qualification
write('openapi.json',api)
# 单费率最多100%；资金上限是合计预算，可显式高于100%，仍受方案总预算约束。
for algorithm,variant in zip(parameters,variants):
    if algorithm not in ('FIXED','UNIT_PRICE','WEIGHTED'):
        variant['properties']['funding_limit_ppm']=minor
write('plan.schema.json',plan)
plan_api=json.loads(json.dumps(plan).replace('#/$defs/condition','#/components/schemas/Condition'))
components['PlanDraft']={k:v for k,v in plan_api.items() if k not in ('$schema','$id','$defs')}
components['PlanDocument']['properties']['rules']=components['PlanDraft']['properties']['rules']
write('openapi.json',api)
# 模拟命令独立可校验Schema，供Go入口直接执行相同契约。
simulation_schema=schema('simulation',json.loads(json.dumps(api['paths']['/v1/simulations']['post']['requestBody']['content']['application/json']['schema']).replace('#/components/schemas/','#/$defs/')))
simulation_schema['$defs']={name:components[name] for name in ('Facts','ActorRef')}
write('simulation.schema.json',simulation_schema)
# 恢复入口落实既有“人工恢复原任务、必须留依据”的能力。
endpoint('/v1/calculation-tasks/{id}/resume','post',obj({'expected_revision':revision,'evidence_ref':identifier,'reason':identifier}),202)
api['paths']['/v1/calculation-tasks/{id}/resume']['post']['responses']['202']['content']['application/json']['schema']=obj({'data':obj({'task_id':identifier,'status':enum('QUEUED')}),'request_id':identifier})
api['paths']['/v1/events']['post']['parameters'].append({'name':'X-Source-Signature','in':'header','required':True,'schema':{'type':'string','pattern':'^[0-9a-fA-F]{64}$'},'description':'本地来源适配器以原始请求字节作HMAC-SHA256；生产来源使用自己的受信适配器'})
api['paths']['/v1/events']['post']['description']='输入event_id为来源事件ID；返回event_id为作用域内不透明接收资源ID，source_event_id保留原来源标识。'
write('openapi.json',api)
errors += ['STATE_CONFLICT','UNAUTHENTICATED','SELF_APPROVAL_FORBIDDEN','NOT_FOUND','RELATION_CYCLE','AMOUNT_OUT_OF_RANGE','REFUND_EXCEEDS_ORIGINAL','NON_MONOTONIC_REFUND','CONSERVATION_VIOLATION','SOURCE_SIGNATURE_INVALID','SNAPSHOT_INTEGRITY_FAILED','LEDGER_NOT_READY']
write('errors.json',{'standard_version':'1','codes':list(dict.fromkeys(errors))})
# 最终HTTP验收：冻结实际资源响应，禁止新增接口误用“approval_id”占位响应。
def actual_response(path,method,code,data):
    op=api['paths'][path][method]
    if str(code) not in op['responses']:
        op['responses'][str(code)]=op['responses'].pop('200')
    op['responses'][str(code)]['content']={'application/json':{'schema':obj({'data':data,'request_id':identifier})}}
actual_response('/v1/audit-logs','get',200,response_obj({'rows':arr({'type':'object'}),'as_of':time,'next_cursor':nullable(identifier)}))
actual_response('/v1/payment-callbacks/{channel_id}','post',200,response_obj({'status':enum('ACCEPTED')}))
actual_response('/v1/platform/catalogs','get',200,response_obj({'versions':arr({'type':'object'})}))
actual_response('/v1/platform/components','get',200,response_obj({'components':arr({'type':'object'})}))
actual_response('/v1/notifications','get',200,response_obj({'rows':arr({'type':'object'}),'as_of':time}))
actual_response('/v1/identity/subjects/{id}/permissions','get',200,response_obj({'subject_id':identifier,'permissions':arr(identifier),'revision':revision}))
actual_response('/v1/agents/{id}','get',200,response_obj({'agent_id':identifier,'user_id':identifier,'status':enum('ACTIVE','FROZEN','DISABLED','CLOSED'),'revision':revision}))
actual_response('/v1/risk-holds/{id}','get',200,response_obj({'risk_id':identifier,'revision':revision,'status':identifier,'restrictions':arr({'type':'object'})}))
actual_response('/v1/risk-holds/{id}/appeals','post',201,response_obj({'appeal_id':identifier,'risk_id':identifier}))
actual_response('/v1/security-events/{id}/handling-records','post',201,response_obj({'record_id':identifier,'revision':revision,'status':enum('OPEN','ACKNOWLEDGED','RESOLVED')}))
statement_response=response_obj({'statement_id':identifier,'agent_id':identifier,'currency':currency,'period':identifier,'status':enum('DRAFT','PENDING_APPROVAL','APPROVED','PARTIALLY_PAID','PAID','CANCELLED'),'revision':revision,'content_hash':identifier,'total_minor':minor,'items':arr({'type':'object'}),'approval_valid':boolean})
actual_response('/v1/statements/{id}','get',200,statement_response)
actual_response('/v1/statement-runs','post',202,response_obj({'run_id':identifier,'period':identifier,'statements':arr(statement_response)}))
actual_response('/v1/reconciliation-runs','post',200,response_obj({'run_id':identifier,'status':enum('COMPLETED')}))
for path in ('/v1/entitlements/{id}','/v1/debts/{id}'):
    actual_response(path,'get',200,response_obj({'entitlement_id':identifier,'currency':currency,'target_minor':minor,'pending_minor':minor,'available_minor':minor,'reserved_minor':minor,'paid_minor':minor,'debt_minor':minor,'recovered_minor':minor,'frozen':boolean,'payment_hold':boolean}))
actual_response('/v1/refunds/{id}','get',200,response_obj({'refund_task_id':identifier,'refund':ref('Refund'),'status':identifier,'reason_code':nullable(identifier)}))
# 历史资格保留来源及区间；已知无合同与缺未知事实不能混为一谈。
snapshot['properties']['qualification_facts']=json.loads(json.dumps(snapshot['properties']['qualification_facts']))
qualified=snapshot['properties']['qualification_facts']['additionalProperties']
qualified['properties'].update({'joined':boolean,'grade_id':identifier,'revision':revision,'source_kind':identifier,'evidence_ref':identifier,'status_effective_from':time,'status_effective_to':nullable(time)})
snapshot['properties']['contract_facts']={'type':'object','additionalProperties':{'oneOf':[obj({'status':enum('ABSENT')}),obj({'status':enum('PRESENT'),'contract_id':identifier,'plan_id':identifier,'effective_from':time,'effective_to':nullable(time),'evidence_ref':identifier},['status','plan_id','effective_from','effective_to'])]}}
snapshot['properties']['relationship_facts']=arr(obj({'agent_id':identifier,'depth':{'type':'integer','minimum':1,'maximum':64},'effective_from':time,'effective_to':nullable(time),'kind':enum('AGENT_RELATION','USER_BINDING'),'evidence_ref':identifier},['agent_id','depth','effective_from','effective_to','kind']))
components['Snapshot']={k:v for k,v in snapshot.items() if k not in ('$schema','$id')}
actual_response('/v1/calculation-tasks/{id}/adjustments','get',200,response_obj({'original_task_id':identifier,'revision':revision,'effective_snapshot':ref('Snapshot'),'original_snapshot_hash':nullable(identifier)}))
# 导出过期与已落地的稳定业务拒绝码。
errors+=['REPORT_EXPIRED','REPORT_TOO_LARGE','REPORT_INCOMPLETE_LINEAGE','INSUFFICIENT_BALANCE','FUNDS_FROZEN','PAYMENT_RESULT_UNKNOWN','PAYMENT_BELOW_MINIMUM','PAYMENT_LIMIT_EXCEEDED','REFUND_QUANTITY_MISSING','REFUND_TARGET_INCREASE','FIFO_DEBT_REQUIRED','EVIDENCE_NOT_CONFIRMED']
write('errors.json',{'standard_version':'1','codes':list(dict.fromkeys(errors))})
write('snapshot.schema.json',snapshot)
write('openapi.json',api)

endpoint('/v1/users/lookup','get',query=[('source_id',identifier,True),('external_user_id',identifier,True)])
actual_response('/v1/users/lookup','get',200,response_obj({'user_id':identifier,'source_id':identifier,'external_user_id':identifier}))
write('openapi.json',api)

api['paths']['/v1/report-exports/{id}/download']['get']['responses']['410']['content']={'application/json':{'schema':ref('Error')}}
write('openapi.json',api)
components['CalculationResult']['additionalProperties']=True
components['CalculationResult']['properties'].update({'reason_code':nullable(identifier),'recovery_url':identifier,'retry_count':{'type':'integer','minimum':0},'revision':revision,'snapshot_hash':nullable(identifier)})
cursor_token={'type':'string','minLength':1,'maxLength':4096}
for path,operations in api['paths'].items():
    for method,operation in operations.items():
        for parameter in operation.get('parameters',[]):
            if parameter['name']=='cursor': parameter['schema']=cursor_token
        for response in operation['responses'].values():
            schema=response.get('content',{}).get('application/json',{}).get('schema',{})
            data=schema.get('properties',{}).get('data',{})
            if 'next_cursor' in data.get('properties',{}): data['properties']['next_cursor']=nullable(cursor_token)
api['paths']['/v1/audit-logs']['get']['parameters'].append({'name':'as_of','in':'query','required':False,'schema':time})
write('openapi.json',api)

# 登录返回直接身份信封；与业务资源的 data 信封分别冻结。
auth_principal=obj({'subject_id':identifier,'tenant_id':identifier,'app_id':identifier})
for path,method,body,code,schema in [
 ('/v1/auth/config','get',None,200,obj({'local_password':boolean,'external_bearer':boolean,'external_login_url':{'type':'null'}})),
 ('/v1/auth/login','post',obj({'username':{'type':'string','minLength':1,'maxLength':128},'password':{'type':'string','minLength':1,'maxLength':1024}}),200,obj({'access_token':identifier,'token_type':enum('Bearer'),'expires_in':{'type':'integer','const':28800},'principal':auth_principal})),
 ('/v1/auth/me','get',None,200,obj({'subject_id':identifier,'tenant_id':identifier,'app_id':identifier,'permissions':arr(identifier),'revision':revision})),
 ('/v1/auth/logout','post',None,204,None)]:
    endpoint(path,method,body,code)
    op=api['paths'][path][method]
    op['parameters']=[]
    if path in ('/v1/auth/config','/v1/auth/login'):op['security']=[]
    op['responses'][str(code)]={'description':'登录会话结果'}
    if schema is not None:op['responses'][str(code)]['content']={'application/json':{'schema':schema}}
errors+=['LOGIN_RATE_LIMITED']
write('errors.json',{'standard_version':'1','codes':list(dict.fromkeys(errors))})
write('openapi.json',api)

# 注册接入是显式启用的服务端能力；不承载会员账号密码或公开管理员注册。
registration_event=obj({'schema_version':enum('1'),'source_id':identifier,'event_id':identifier,'external_user_id':identifier,'display_name':identifier,'registered_at':time,'invitation_code':nullable(identifier)},['schema_version','source_id','event_id','external_user_id','display_name','registered_at'])
endpoint('/v1/registrations','post',registration_event)
api['paths']['/v1/registrations']['post']['parameters'].append({'name':'X-Registration-Signature','in':'header','required':True,'schema':{'type':'string','pattern':'^[0-9a-fA-F]{64}$'}})
api['paths']['/v1/registrations']['post']['description']='受控来源注册事实接入；必须启用 LOCAL_REGISTRATION_ENABLED，来源验证后原子创建代理和首次关系。'
actual_response('/v1/registrations','post',200,response_obj({'source_id':identifier,'event_id':identifier,'external_user_id':identifier,'user_id':identifier,'agent_id':identifier,'parent_agent_id':nullable(identifier),'registered_at':time}))
endpoint('/v1/registration-invitations','post',obj({'source_id':identifier,'agent_id':identifier}))
actual_response('/v1/registration-invitations','post',200,response_obj({'invitation_code':identifier,'source_id':identifier,'agent_id':identifier,'created_at':time,'registration_url':nullable({'type':'string','format':'uri'})}))
write('openapi.json',api)

# 独立有效会员事实契约：正向成功事实与不可变规则，不能冒充资金或计佣事件。
qualification_policy=obj({'policy_id':identifier,'source_id':identifier,'currency':currency,'deposit_threshold_minor':{'type':'string','pattern':'^[1-9][0-9]{0,37}$'},'valid_bet_threshold_minor':{'type':'string','pattern':'^[1-9][0-9]{0,37}$'},'venue_types':{'type':'array','items':identifier,'minItems':1,'maxItems':64,'uniqueItems':True}})
qualification_state=response_obj({'policy_id':identifier,'source_id':identifier,'external_user_id':identifier,'user_id':identifier,'agent_id':identifier,'parent_agent_id':nullable(identifier),'currency':currency,'deposit_total_minor':minor,'valid_bet_total_minor':minor,'deposit_threshold_minor':minor,'valid_bet_threshold_minor':minor,'is_effective':boolean,'qualified_at':nullable(time),'first_observed_at':nullable(time),'revision':revision})
qualification_fact=obj({'schema_version':enum('1'),'policy_id':identifier,'source_id':identifier,'event_id':identifier,'business_id':identifier,'event_type':enum('DEPOSIT_SUCCEEDED','BET_SETTLED'),'external_user_id':identifier,'business_time':time,'amount_minor':{'type':'string','pattern':'^(0|[1-9][0-9]{0,37})$'},'currency':currency,'venue_type':nullable(identifier)})
endpoint('/v1/member-qualification-policies','post',qualification_policy)
actual_response('/v1/member-qualification-policies','post',200,qualification_policy)
endpoint('/v1/member-qualification-facts','post',qualification_fact)
api['paths']['/v1/member-qualification-facts']['post']['parameters'].append({'name':'X-Qualification-Signature','in':'header','required':True,'schema':{'type':'string','pattern':'^[0-9a-fA-F]{64}$'}})
actual_response('/v1/member-qualification-facts','post',200,qualification_state)
endpoint('/v1/member-qualifications','get',query=[('policy_id',identifier,True),('source_id',identifier,True),('external_user_id',identifier,True)])
actual_response('/v1/member-qualifications','get',200,qualification_state)
write('openapi.json',api)
