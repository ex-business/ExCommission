CREATE TABLE IF NOT EXISTS settlement_schedule (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL,
 next_period CHAR(10) NOT NULL, last_result LONGBLOB NULL, updated_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id)
) ENGINE=InnoDB;
