CREATE TABLE IF NOT EXISTS ledger_entitlements (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, entitlement_id CHAR(64) NOT NULL,
 task_id VARCHAR(128) NOT NULL, source_id VARCHAR(128) NOT NULL, business_id VARCHAR(128) NOT NULL,
 event_type VARCHAR(32) NOT NULL, plan_version_id VARCHAR(128) NOT NULL, rule_id VARCHAR(128) NOT NULL,
 beneficiary_id VARCHAR(128) NOT NULL, currency VARCHAR(32) NOT NULL,
 original_target DECIMAL(38,0) NOT NULL, target DECIMAL(38,0) NOT NULL,
 pending DECIMAL(38,0) NOT NULL, available DECIMAL(38,0) NOT NULL DEFAULT 0,
 reserved DECIMAL(38,0) NOT NULL DEFAULT 0, paid DECIMAL(38,0) NOT NULL DEFAULT 0,
 debt DECIMAL(38,0) NOT NULL DEFAULT 0, recovered DECIMAL(38,0) NOT NULL DEFAULT 0,
 business_time DATETIME(6) NOT NULL, posted_at DATETIME(6) NOT NULL, revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
 PRIMARY KEY(tenant_id,app_id,entitlement_id), KEY beneficiary(tenant_id,app_id,beneficiary_id,currency),
 CHECK(pending>=0 AND available>=0 AND reserved>=0 AND paid>=0 AND debt>=0 AND recovered>=0 AND target>=0)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS ledger_journals (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, journal_id CHAR(64) NOT NULL,
 business_ref VARCHAR(128) NOT NULL, idempotency_key VARCHAR(128) NOT NULL, currency VARCHAR(32) NOT NULL,
 debit_account VARCHAR(64) NOT NULL, credit_account VARCHAR(64) NOT NULL, amount DECIMAL(38,0) NOT NULL,
 occurred_at DATETIME(6) NOT NULL, posted_at DATETIME(6) NOT NULL, reversal_of CHAR(64) NULL,
 PRIMARY KEY(tenant_id,app_id,journal_id), UNIQUE KEY once_only(tenant_id,app_id,idempotency_key), CHECK(amount>0)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS ledger_restrictions (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, entitlement_id CHAR(64) NOT NULL,
 risk_id VARCHAR(128) NOT NULL, reason_code VARCHAR(128) NOT NULL, created_at DATETIME(6) NOT NULL, closed_at DATETIME(6) NULL,
 PRIMARY KEY(tenant_id,app_id,entitlement_id,risk_id)
) ENGINE=InnoDB;
