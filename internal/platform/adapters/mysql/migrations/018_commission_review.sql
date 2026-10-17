CREATE TABLE IF NOT EXISTS payout_instructions (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, instruction_id VARCHAR(128) NOT NULL,
 statement_id VARCHAR(128) NOT NULL, status VARCHAR(32) NOT NULL, revision BIGINT UNSIGNED NOT NULL,
 body LONGBLOB NOT NULL, active_slot TINYINT NULL, created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,instruction_id), UNIQUE KEY active_instruction(tenant_id,app_id,statement_id,active_slot)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS workflow_decision_notes (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, approval_id VARCHAR(128) NOT NULL,
 revision BIGINT UNSIGNED NOT NULL, subject_id VARCHAR(128) NOT NULL, note TEXT NOT NULL, created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,approval_id,revision)
) ENGINE=InnoDB;
