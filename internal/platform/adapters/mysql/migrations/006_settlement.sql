CREATE TABLE IF NOT EXISTS settlement_spaces (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL,
 PRIMARY KEY(tenant_id,app_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS settlement_runs (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, period CHAR(10) NOT NULL,
 run_id VARCHAR(128) NOT NULL, calendar_version VARCHAR(128) NOT NULL, filter_hash CHAR(64) NOT NULL, created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,period), UNIQUE KEY run_id(tenant_id,app_id,run_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS settlement_statements (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, statement_id VARCHAR(128) NOT NULL,
 beneficiary_id VARCHAR(128) NOT NULL, currency VARCHAR(32) NOT NULL, period CHAR(10) NOT NULL,
 status VARCHAR(32) NOT NULL, revision BIGINT UNSIGNED NOT NULL, content_hash CHAR(64) NOT NULL,
 total DECIMAL(38,0) NOT NULL, created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,statement_id), UNIQUE KEY statement_period(tenant_id,app_id,beneficiary_id,currency,period)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS settlement_items (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, statement_id VARCHAR(128) NOT NULL,
 entitlement_id CHAR(64) NOT NULL, amount DECIMAL(38,0) NOT NULL, entitlement_revision BIGINT UNSIGNED NOT NULL,
 PRIMARY KEY(tenant_id,app_id,statement_id,entitlement_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS settlement_claims (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, entitlement_id CHAR(64) NOT NULL,
 statement_id VARCHAR(128) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,entitlement_id)
) ENGINE=InnoDB;
