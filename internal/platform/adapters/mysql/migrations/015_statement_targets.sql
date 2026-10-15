CREATE TABLE IF NOT EXISTS settlement_item_targets (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, statement_id VARCHAR(128) NOT NULL,
 entitlement_id CHAR(64) NOT NULL, target DECIMAL(38,0) NOT NULL, paid_before DECIMAL(38,0) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,statement_id,entitlement_id)
) ENGINE=InnoDB;
