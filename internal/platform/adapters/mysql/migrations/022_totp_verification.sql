CREATE TABLE IF NOT EXISTS security_totp_attempts (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, subject_id VARCHAR(128) NOT NULL,
 minute_window BIGINT NOT NULL, attempts INT UNSIGNED NOT NULL,
 PRIMARY KEY(tenant_id,app_id,subject_id,minute_window)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS security_totp_uses (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, subject_id VARCHAR(128) NOT NULL,
 time_counter BIGINT NOT NULL, action_hash CHAR(64) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,subject_id)
) ENGINE=InnoDB;
