CREATE TABLE IF NOT EXISTS calculation_refund_spaces (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, source_id VARCHAR(128) NOT NULL,
 business_id VARCHAR(128) NOT NULL, event_type VARCHAR(32) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,source_id,business_id,event_type)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS calculation_refunds (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, source_id VARCHAR(128) NOT NULL,
 refund_id VARCHAR(128) NOT NULL, business_id VARCHAR(128) NOT NULL, event_type VARCHAR(32) NOT NULL,
 refund_task_id VARCHAR(128) NOT NULL, canonical_body LONGBLOB NOT NULL, payload LONGBLOB NOT NULL,
 status VARCHAR(64) NOT NULL, reason_code VARCHAR(128) NULL, created_at DATETIME(6) NOT NULL, updated_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,source_id,refund_id), UNIQUE KEY task_id(tenant_id,app_id,refund_task_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS ledger_debt_ages (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, entitlement_id CHAR(64) NOT NULL,
 created_at DATETIME(6) NOT NULL, PRIMARY KEY(tenant_id,app_id,entitlement_id)
) ENGINE=InnoDB;
