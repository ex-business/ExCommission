import json
import unittest
from pathlib import Path
from copy import deepcopy
from jsonschema import Draft202012Validator, FormatChecker
from openapi_spec_validator import validate
ROOT=Path(__file__).resolve().parents[1]
class Contracts(unittest.TestCase):
    def setUp(self):
        self.schema=json.loads((ROOT/'contracts/v1/event.schema.json').read_text())
        self.validator=Draft202012Validator(self.schema,format_checker=FormatChecker())
        self.event={'schema_version':'1','source_id':'S1','event_id':'E1','business_id':'O1','event_type':'FEE_CONFIRMED','business_time':'2026-10-01T08:00:00Z','external_user_id':'U1','product_code':'BTC-USDT','scene':'SPOT','source_revision':'1','payload':{'basis_amount':'1000','basis_currency':'USD'}}
    def test_schemas_and_openapi(self):
        for path in (ROOT/'contracts/v1').glob('*.schema.json'):
            Draft202012Validator.check_schema(json.loads(path.read_text()))
        validate(json.loads((ROOT/'contracts/v1/openapi.json').read_text()))
    def test_milestone_examples_and_reserved_source_type(self):
        for name,example in [('milestone-program','milestone-program'),('milestone-observation','milestone-observation'),('plan','plan-milestone')]:
            definition=json.loads((ROOT/f'contracts/v1/{name}.schema.json').read_text())
            value=json.loads((ROOT/f'conformance/v1/{example}.json').read_text())
            Draft202012Validator(definition,format_checker=FormatChecker()).validate(value)
        source=json.loads((ROOT/'contracts/v1/event.schema.json').read_text())
        self.assertNotIn('MILESTONE_REACHED',source['properties']['event_type']['enum'])
        event=deepcopy(self.event);event['event_type']='MILESTONE_REACHED'
        self.assertTrue(list(self.validator.iter_errors(event)))
    def test_standard_traceability(self):
        manifest=json.loads((ROOT/'conformance/v1/manifest.json').read_text())
        rows=manifest['requirements']
        self.assertEqual(len(rows),len({r['id'] for r in rows}))
        self.assertEqual(len(rows),len({r['planned_test_id'] for r in rows}))
        self.assertTrue(all(r['implementation_task'] in {'%02d'%i for i in range(1,16)} for r in rows))
        self.assertEqual(len(manifest['state_scenarios']),20)
        self.assertEqual(len(manifest['source_hashes']),9)
    def test_standard_example(self): self.validator.validate(self.event)
    def test_unknown_fields_rejected_at_both_levels(self):
        for where in ('root','payload'):
            value=deepcopy(self.event)
            (value if where=='root' else value['payload'])['unexpected']=True
            self.assertTrue(list(self.validator.iter_errors(value)))
    def test_amount_boundaries(self):
        for amount in ('-1','+1','1e3','1,000','0.000000001','1000000000000000000',None,1.5):
            with self.subTest(amount=amount):
                value=deepcopy(self.event);value['payload']['basis_amount']=amount
                self.assertTrue(list(self.validator.iter_errors(value)))
        for amount in ('0','999999999999999999.99999999','0.00000001'):
            value=deepcopy(self.event);value['payload']['basis_amount']=amount
            self.validator.validate(value)
    def test_revision_and_confirmation(self):
        value=deepcopy(self.event);value['source_revision']='2'
        self.assertTrue(list(self.validator.iter_errors(value)))
        value=deepcopy(self.event);value['payload']['confirmed']=False
        self.assertTrue(list(self.validator.iter_errors(value)))
    def test_quantity_requires_unit(self):
        value=deepcopy(self.event);value['payload']['quantity']='1'
        self.assertTrue(list(self.validator.iter_errors(value)))
        value['payload']['quantity_unit']='LOT';self.validator.validate(value)
    def test_time_and_required_fields(self):
        for time in ('2026-02-30T00:00:00Z','2026-10-01T08:00:00+08:00','2026-10-01T08:00:00.1234567Z'):
            value=deepcopy(self.event);value['business_time']=time
            self.assertTrue(list(self.validator.iter_errors(value)))
        for key in self.event:
            value=deepcopy(self.event);del value[key]
            self.assertTrue(list(self.validator.iter_errors(value)))
class PlanContracts(unittest.TestCase):
    def setUp(self):
        schema=json.loads((ROOT/'contracts/v1/plan.schema.json').read_text())
        self.validator=Draft202012Validator(schema)
        self.plan={'scope':{'type':'ALL_ACTIVE_AGENTS','agent_ids':[]},'plan_id':'P1','name':'示例','revision':0,'standard_version':'1','route':{'product_code':'BTC-USDT','event_type':'FEE_CONFIRMED','scene':'SPOT'},'currency':'USD','calendar_version':'UTC-v1','agent_policy':{'max_depth':3,'self_reward':False,'earn_while_frozen':False,'require_contract':False,'grade_window':'PREVIOUS_CALENDAR_MONTH','grade_evaluation':'DAILY','eligibility_required':True},'grade_definitions':[{'id':'G0','rank':0,'conditions':[]}],'rules':[{'id':'R1','algorithm_id':'PERCENT','algorithm_version':'1','recipient':'DIRECT','condition':{'logic':'AND','children':[]},'parameters':{'rate_ppm':'100000'},'funding_limit_ppm':'1000000','per_beneficiary_cap_minor':None,'per_event_total_cap_minor':None,'frequency':'ONCE_PER_BUSINESS_RULE'}],'event_budget_minor':None,'settlement_policy':{'period':'DAILY','minimum_payout_minor':'0','max_payment_minor':None,'late_policy':'NEXT_OPEN_PERIOD','channel_fee_bearer':'PLATFORM'}}
    def test_all_algorithm_parameter_shapes(self):
        cases={
          'PERCENT':{'rate_ppm':'100000'},
          'FIXED':{'amount_minor':'100','reward_currency':'USD'},
          'UNIT_PRICE':{'unit_price':'5','quantity_unit':'LOT','reward_currency':'USD'},
          'INDEPENDENT':{'rates':[{'product_code':'BTC-USDT','grade_id':'G0','depth':1,'rate_ppm':'100000'}]},
          'DIFFERENTIAL':{'rates':[{'product_code':'BTC-USDT','grade_id':'G0','rate_ppm':'100000'}]},
          'MARGINAL':{'tiers':[{'lower':'0','upper':None,'rate_ppm':'100000'}]},
          'HIGHEST_RATE':{'tiers':[{'lower':'0','upper':None,'rate_ppm':'100000'}]},
          'WEIGHTED':{'pool_minor':'100','reward_currency':'USD','beneficiaries':[{'beneficiary_id':'A1','weight':'1'}]}}
        for algorithm,parameters in cases.items():
            with self.subTest(algorithm=algorithm):
                plan=deepcopy(self.plan);rule=plan['rules'][0]
                rule['algorithm_id']=algorithm;rule['parameters']=parameters
                if algorithm in ('FIXED','UNIT_PRICE','WEIGHTED'):
                    rule['funding_limit_ppm']=None;plan['event_budget_minor']='10000'
                if algorithm=='WEIGHTED': rule['recipient']='EXPLICIT'
                self.validator.validate(plan)
                rule['parameters']['unexpected']='x'
                self.assertTrue(list(self.validator.iter_errors(plan)))
    def test_all_operations_have_envelopes_and_auth(self):
        api=json.loads((ROOT/'contracts/v1/openapi.json').read_text())
        count=0
        for path,operations in api['paths'].items():
            for method,operation in operations.items():
                count+=1
                self.assertIn('409',operation['responses'])
                if method!='get' and not path.startswith(('/v1/payment-callbacks/', '/v1/auth/')):
                    self.assertTrue(any(p['name']=='Idempotency-Key' and p['required'] for p in operation['parameters']))
                if method=='get':
                    self.assertFalse(any(p['name']=='Idempotency-Key' for p in operation['parameters']))
        self.assertGreaterEqual(count,40)
        for path in ('/v1/auth/login','/v1/auth/logout'):
            self.assertFalse(any(p['name']=='Idempotency-Key' for p in api['paths'][path]['post']['parameters']))
        self.assertEqual(api['paths']['/v1/auth/login']['post']['security'],[])
        self.assertEqual(api['paths']['/v1/auth/config']['get']['security'],[])
        self.assertNotIn('security',api['paths']['/v1/auth/me']['get'])
        self.assertNotIn('content',api['paths']['/v1/auth/logout']['post']['responses']['204'])
    def test_condition_metric_types(self):
        cases=[{'metric':'confirmed','operator':'EQ','value':'true','unit':'BOOLEAN'},
               {'metric':'confirmed','operator':'GT','value':True,'unit':'BOOLEAN'},
               {'metric':'scene','operator':'GT','value':'SPOT','unit':'ENUM'},
               {'metric':'business_time','operator':'EQ','value':'2026-10-01','unit':'UTC'}]
        for condition in cases:
            plan=deepcopy(self.plan);plan['rules'][0]['condition']=condition
            self.assertTrue(list(self.validator.iter_errors(plan)))
    def test_scope(self):
        for scope in ({'type':'ALL_ACTIVE_AGENTS','agent_ids':['A1']},{'type':'EXPLICIT_AGENT_IDS','agent_ids':[]},{'type':'EXPLICIT_AGENT_IDS','agent_ids':['A1','A1']}):
            plan=deepcopy(self.plan);plan['scope']=scope
            self.assertTrue(list(self.validator.iter_errors(plan)))
        plan=deepcopy(self.plan);plan['scope']={'type':'EXPLICIT_AGENT_IDS','agent_ids':['A1']}
        self.validator.validate(plan)
    def test_percent_plan(self): self.validator.validate(self.plan)
    def test_unknown_and_unsupported_configuration(self):
        for change in ('unknown','self_reward','depth','algorithm','frequency','over_rate','wrong_parameters'):
            with self.subTest(change=change):
                plan=deepcopy(self.plan);rule=plan['rules'][0]
                if change=='unknown': plan['unknown']=1
                elif change=='self_reward': plan['agent_policy']['self_reward']=True
                elif change=='depth': plan['agent_policy']['max_depth']=65
                elif change=='algorithm': rule['algorithm_id']='SCRIPT'
                elif change=='frequency': rule['frequency']='MONTHLY'
                elif change=='over_rate': rule['parameters']['rate_ppm']='1000001'
                else: rule['parameters']={'amount_minor':'100'}
                self.assertTrue(list(self.validator.iter_errors(plan)))
    def test_not_requires_exactly_one_child(self):
        plan=deepcopy(self.plan)
        leaf={'metric':'confirmed','operator':'EQ','value':True,'unit':'BOOLEAN'}
        for children in ([],[leaf,leaf]):
            plan['rules'][0]['condition']={'logic':'NOT','children':children}
            self.assertTrue(list(self.validator.iter_errors(plan)))
        plan['rules'][0]['condition']={'logic':'NOT','children':[leaf]}
        self.validator.validate(plan)

class MemberStatisticsContracts(unittest.TestCase):
    def test_configurable_periods_and_legacy_compatibility(self):
        api=json.loads((ROOT/'contracts/v1/openapi.json').read_text())
        schema=api['paths']['/v1/member-qualification-policies']['post']['requestBody']['content']['application/json']['schema']
        validator=Draft202012Validator(schema)
        policy={'policy_id':'q1','source_id':'sandbox','currency':'USD','deposit_threshold_minor':'5000','valid_bet_threshold_minor':'10000','venue_types':['SPORTS']}
        validator.validate(policy)
        for period in ('NATURAL_DAY','NATURAL_WEEK','NATURAL_MONTH','FOREVER'):
            policy['member_statistics']={'range':period,'timezone':'Asia/Shanghai'}
            validator.validate(policy)
        policy['member_statistics']['range']='ROLLING_WEEK'
        self.assertTrue(list(validator.iter_errors(policy)))
        policy['member_statistics']={'range':'NATURAL_DAY'}
        self.assertTrue(list(validator.iter_errors(policy)))

class ReferralContracts(unittest.TestCase):
    def test_positive_minor_amounts_and_thresholds(self):
        api=json.loads((ROOT/'contracts/v1/openapi.json').read_text())
        schema=api['paths']['/v1/referral-reward-rules']['post']['requestBody']['content']['application/json']['schema']
        validator=Draft202012Validator(schema)
        rule={'rule_id':'r1','name':'推荐奖励','plan_id':'P1','source_id':'sandbox','qualification_policy_id':'q1','currency':'USD','tiers':[{'tier_id':'one','required_direct_members':1,'amount_minor':'500'}]}
        validator.validate(rule)
        for amount in ('0','-1','1.5','01'):
            rule['tiers'][0]['amount_minor']=amount
            self.assertTrue(list(validator.iter_errors(rule)))
        rule['tiers'][0]['amount_minor']='500'
        rule['tiers'][0]['required_direct_members']=0
        self.assertTrue(list(validator.iter_errors(rule)))

if __name__=='__main__': unittest.main(verbosity=2)
