"""补充执行路由模块契约；仅维护本模块路径，保留既有接口定义和顺序。"""
import json
from pathlib import Path
p = Path(__file__).resolve().parents[1] / 'contracts/v1/openapi.json'
api = json.loads(p.read_text())
text = {'type': 'string', 'minLength': 1, 'maxLength': 128}
revision = {'type': 'integer', 'minimum': 0}
def obj(properties, required=None):
    return {'type': 'object', 'properties': properties, 'required': list(properties) if required is None else required, 'additionalProperties': False}
rule = obj({'id': text, 'source_id': text, 'event_kind': text, 'engine': {'type': 'string', 'enum': ['GENERAL', 'AFFILIATE']}, 'effective_from': {'type': 'string', 'format': 'date-time'}})
config = obj({'rules': {'type': 'array', 'maxItems': 1024, 'items': rule}})
change = obj({'expected_revision': revision, 'config': config, 'reason': {'type': 'string', 'minLength': 1, 'maxLength': 1000}})
def add(path, method, operation, summary, permission, schema=None, success=200):
    value = {'operationId': operation, 'summary': summary, 'description': '按已认证站点隔离；执行路由配置独立审批，历史归属不可改写。', 'x-permission': permission, 'parameters': [], 'responses': {str(code): {'description': description} for code, description in [(success, '请求成功'), (400, '输入非法'), (401, '未认证'), (403, '权限不足或本人审批'), (409, '修订或执行归属冲突'), (503, '依赖不可用')]}}
    if '{id}' in path:
        value['parameters'] = [{'name': 'id', 'in': 'path', 'required': True, 'schema': text}]
    if method == 'post':
        value.setdefault('parameters', []).append({'name': 'Idempotency-Key', 'in': 'header', 'required': True, 'schema': {'type': 'string', 'minLength': 1, 'maxLength': 128}})
        value['requestBody'] = {'required': True, 'content': {'application/json': {'schema': schema}}}
    api['paths'].setdefault(path, {})[method] = value
add('/v1/execution-routing/config', 'get', 'readExecutionRouting', '读取执行路由', 'plans:read')
add('/v1/execution-routing/config-requests', 'post', 'requestExecutionRouting', '申请执行路由配置', 'plans:write', change, 202)
add('/v1/execution-routing/config-requests/{id}', 'get', 'readExecutionRoutingRequest', '读取执行路由申请', 'plans:read')
add('/v1/execution-routing/preview', 'post', 'previewExecutionRouting', '预览业务执行引擎', 'plans:read', obj({'config': config, 'source_id': text, 'event_kind': text, 'business_time': {'type': 'string', 'format': 'date-time'}}))
add('/v1/execution-routing/records', 'get', 'readExecutionRoutingRecords', '执行归属记录', 'plans:read')
# 成功响应明确区分只读预览、审批受理与已登记业务；未命中的rule为null。
record = obj({'source_id': text, 'event_id': text, 'business_id': text, 'event_kind': text, 'engine': {'type':'string','enum':['GENERAL','AFFILIATE']}, 'route_id': text, 'config_revision':revision, 'business_time':{'type':'string','format':'date-time'}, 'received_at':{'type':'string','format':'date-time'}})
responses = {
 '/v1/execution-routing/config': (200,obj({'revision':revision,'config':config})),
 '/v1/execution-routing/config-requests': (202,obj({'approval_id':text,'object_hash':text,'revision':revision})),
 '/v1/execution-routing/config-requests/{id}': (200,obj({'approval':{'type':'object'},'change':change})),
 '/v1/execution-routing/preview': (200,obj({'preview_only':{'const':True},'matched':{'type':'boolean'},'rule':{'anyOf':[rule,{'type':'null'}]}})),
 '/v1/execution-routing/records': (200,obj({'rows':{'type':'array','items':record}})),
}
for path,(code,data) in responses.items():
    method = 'post' if path.endswith(('/config-requests','/preview')) else 'get'
    api['paths'][path][method]['responses'][str(code)]['content'] = {'application/json':{'schema':obj({'request_id':text,'data':data})}}
# 补齐既有数据接入接口的冲突响应说明，符合统一契约校验；不改变服务行为。
for path, ops in api['paths'].items():
    if path.startswith('/v1/affiliate/ingestion/'):
        for method, operation in ops.items():
            operation['responses'].setdefault('409', {'description': '幂等、修订或当前状态冲突'})
            operation.setdefault('parameters', [])
# 原方案预检同样经BFF校验幂等头，补齐其既有请求契约声明。
preview = api['paths']['/v1/affiliate/config-preview']['post']
preview.setdefault('parameters', [{'name': 'Idempotency-Key', 'in': 'header', 'required': True, 'schema': {'type': 'string', 'minLength': 1, 'maxLength': 128}}])
p.write_text(json.dumps(api, ensure_ascii=False, indent=2) + '\n')
