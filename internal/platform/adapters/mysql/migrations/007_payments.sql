CREATE TABLE IF NOT EXISTS payment_orders (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, payment_id VARCHAR(128) NOT NULL,
 statement_id VARCHAR(128) NOT NULL, merchant_payment_id CHAR(64) NOT NULL, channel_id VARCHAR(128) NOT NULL,
 currency VARCHAR(32) NOT NULL, amount DECIMAL(38,0) NOT NULL, status VARCHAR(32) NOT NULL,
 reconciliation_status VARCHAR(64) NOT NULL, receipt_ref VARCHAR(128) NULL, revision BIGINT UNSIGNED NOT NULL DEFAULT 0,
 created_at DATETIME(6) NOT NULL, updated_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,payment_id), UNIQUE KEY merchant_id(merchant_payment_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS payment_allocations (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, payment_id VARCHAR(128) NOT NULL,
 entitlement_id CHAR(64) NOT NULL, amount DECIMAL(38,0) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,payment_id,entitlement_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS payment_callbacks (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, channel_id VARCHAR(128) NOT NULL,
 callback_id VARCHAR(128) NOT NULL, body_hash CHAR(64) NOT NULL, payment_id VARCHAR(128) NOT NULL,
 received_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,channel_id,callback_id)
) ENGINE=InnoDB;
