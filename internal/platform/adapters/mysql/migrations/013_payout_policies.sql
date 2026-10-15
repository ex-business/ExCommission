CREATE TABLE IF NOT EXISTS ledger_payout_policies (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, entitlement_id CHAR(64) NOT NULL,
 minimum DECIMAL(38,0) NOT NULL, maximum DECIMAL(38,0) NULL,
 PRIMARY KEY(tenant_id,app_id,entitlement_id)
) ENGINE=InnoDB;
INSERT IGNORE INTO ledger_payout_policies SELECT e.tenant_id,e.app_id,e.entitlement_id,CAST(JSON_UNQUOTE(JSON_EXTRACT(t.snapshot,'$.Plan.settlement_policy.minimum_payout_minor')) AS DECIMAL(38,0)),CASE WHEN JSON_TYPE(JSON_EXTRACT(t.snapshot,'$.Plan.settlement_policy.max_payment_minor'))='STRING' THEN CAST(JSON_UNQUOTE(JSON_EXTRACT(t.snapshot,'$.Plan.settlement_policy.max_payment_minor')) AS DECIMAL(38,0)) ELSE NULL END FROM ledger_entitlements e JOIN calculation_tasks t ON t.tenant_id=e.tenant_id AND t.app_id=e.app_id AND t.task_id=e.task_id WHERE JSON_TYPE(JSON_EXTRACT(t.snapshot,'$.Plan.settlement_policy.minimum_payout_minor'))='STRING';
CREATE TABLE IF NOT EXISTS settlement_payout_policies (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, statement_id VARCHAR(128) NOT NULL,
 minimum DECIMAL(38,0) NOT NULL, maximum DECIMAL(38,0) NULL,
 PRIMARY KEY(tenant_id,app_id,statement_id)
) ENGINE=InnoDB;
