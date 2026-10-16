CREATE TABLE IF NOT EXISTS member_statistics (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, user_id VARCHAR(128) NOT NULL,
 currency VARCHAR(32) NOT NULL, observed_at DATETIME(6) NOT NULL, body LONGBLOB NOT NULL,
 PRIMARY KEY(tenant_id,app_id,user_id,currency,observed_at)
) ENGINE=InnoDB;
