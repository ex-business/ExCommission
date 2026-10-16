CREATE TABLE IF NOT EXISTS wallet_accounts (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, user_id VARCHAR(128) NOT NULL,
 currency VARCHAR(32) NOT NULL, balance DECIMAL(38,0) NOT NULL, revision BIGINT UNSIGNED NOT NULL,
 PRIMARY KEY(tenant_id,app_id,user_id,currency)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS wallet_transactions (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, transaction_id VARCHAR(128) NOT NULL,
 user_id VARCHAR(128) NOT NULL, currency VARCHAR(32) NOT NULL, amount DECIMAL(38,0) NOT NULL,
 business_ref VARCHAR(128) NOT NULL, payment_id VARCHAR(128) NOT NULL, statement_id VARCHAR(128) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,transaction_id), UNIQUE KEY wallet_reference(tenant_id,app_id,business_ref),
 KEY member_transactions(tenant_id,app_id,user_id,currency,transaction_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS wallet_changes (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, change_id VARCHAR(128) NOT NULL,
 user_id VARCHAR(128) NOT NULL, currency VARCHAR(32) NOT NULL, amount DECIMAL(38,0) NOT NULL,
 balance_before DECIMAL(38,0) NOT NULL, balance_after DECIMAL(38,0) NOT NULL, created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,change_id), KEY member_changes(tenant_id,app_id,user_id,currency,change_id)
) ENGINE=InnoDB;
