"""Validate actual sandbox HTTP requests/responses against the frozen OpenAPI.
Only local component references are resolved. Evidence must omit credential values.
"""
from pathlib import Path
import json
import re
import sys
import warnings
from urllib.parse import urlsplit, parse_qs, unquote
warnings.filterwarnings('ignore', category=DeprecationWarning)
from jsonschema import Draft202012Validator, FormatChecker, RefResolver

root = Path(__file__).resolve().parents[1]
api = json.loads((root / 'contracts/v1/openapi.json').read_text())
evidence = json.loads(Path(sys.argv[1]).read_text())
resolver = RefResolver.from_schema(api)
checker = FormatChecker()
operations = set()
requests = responses = 0
for index, item in enumerate(evidence, 1):
    method, path = item['method'].lower(), item['path_template']
    if path not in api['paths'] or method not in api['paths'][path]:
        raise SystemExit(f'{index}: undeclared operation {method} {path}')
    operation = api['paths'][path][method]
    operations.add(method + ' ' + path)
    code = str(item['status'])
    if code not in operation['responses']:
        raise SystemExit(f'{index}: undeclared response {method} {path} {code}')
    successful = item['status'] < 400
    if successful:
        headers = {name.lower() for name in item['header_names']}
        for parameter in operation.get('parameters', []):
            if parameter.get('required') and parameter['in'] == 'header' and parameter['name'].lower() not in headers:
                raise SystemExit(f'{index}: missing required header name')
        parts = re.split(r'(\{[^}]+\})', path)
        pattern = '^' + ''.join('([^/]+)' if part.startswith('{') else re.escape(part) for part in parts) + '$'
        match = re.match(pattern, urlsplit(item['url']).path)
        if not match:
            raise SystemExit(f'{index}: URL does not match operation template')
        path_values = dict(zip(re.findall(r'\{([^}]+)\}', path), map(unquote, match.groups())))
        for parameter in operation.get('parameters', []):
            if parameter['in'] == 'path':
                Draft202012Validator(parameter['schema'], resolver=resolver, format_checker=checker).validate(path_values[parameter['name']])
        query = parse_qs(urlsplit(item['url']).query)
        for parameter in operation.get('parameters', []):
            if parameter['in'] == 'query':
                name = parameter['name']
                if parameter.get('required') and name not in query:
                    raise SystemExit(f'{index}: missing query {name}')
                if name in query:
                    value = query[name][0]
                    if parameter['schema'].get('type') == 'integer':
                        value = int(value)
                    Draft202012Validator(parameter['schema'], resolver=resolver, format_checker=checker).validate(value)
        body = operation.get('requestBody')
        if body and 'application/json' in body['content']:
            schema = body['content']['application/json']['schema']
            try:
                Draft202012Validator(schema, resolver=resolver, format_checker=checker).validate(item['request'])
            except Exception as error:
                raise SystemExit(f'{index}: request {method} {path}: {error}')
            requests += 1
    response = operation['responses'][code]
    content_type = item['content_type'].split(';')[0]
    if content_type not in response.get('content', {}):
        raise SystemExit(f'{index}: undeclared content type {content_type}: {method} {path} {code}')
    schema = response['content'][content_type]['schema']
    value = item.get('response') if content_type == 'application/json' else item.get('text')
    try:
        Draft202012Validator(schema, resolver=resolver, format_checker=checker).validate(value)
    except Exception as error:
        raise SystemExit(f'{index}: response {method} {path} {code}: {error}')
    responses += 1
result = {'status': 'PASS', 'http_calls': len(evidence), 'validated_request_bodies': requests,
          'validated_responses': responses, 'distinct_operations': len(operations),
          'operations': sorted(operations), 'credential_values_recorded': False}
print(json.dumps(result, ensure_ascii=False))
if len(sys.argv) > 2:
    Path(sys.argv[2]).write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
