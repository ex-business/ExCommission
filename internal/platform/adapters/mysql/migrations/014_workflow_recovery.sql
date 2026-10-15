CREATE TABLE IF NOT EXISTS workflow_execution_attempts (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, attempt_id VARCHAR(128) NOT NULL,
 approval_id VARCHAR(128) NOT NULL, original_revision BIGINT UNSIGNED NOT NULL,
 subject_id VARCHAR(128) NOT NULL, reason VARCHAR(128) NOT NULL, evidence_ref VARCHAR(128) NOT NULL,
 execution_status VARCHAR(32) NOT NULL, occurred_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,attempt_id), UNIQUE KEY execution_revision(tenant_id,app_id,approval_id,original_revision)
) ENGINE=InnoDB;
