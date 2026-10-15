CREATE TABLE IF NOT EXISTS calculation_refund_order (
 receipt_sequence BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, refund_task_id VARCHAR(128) NOT NULL,
 PRIMARY KEY(receipt_sequence), UNIQUE KEY refund_order(tenant_id,app_id,refund_task_id)
) ENGINE=InnoDB;
INSERT IGNORE INTO calculation_refund_order(tenant_id,app_id,refund_task_id)
 SELECT tenant_id,app_id,refund_task_id FROM calculation_refunds ORDER BY created_at,refund_task_id;
CREATE TABLE IF NOT EXISTS calculation_adjustments (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, adjustment_id VARCHAR(128) NOT NULL,
 original_task_id VARCHAR(128) NOT NULL, revision BIGINT UNSIGNED NOT NULL,
 snapshot LONGBLOB NOT NULL, result LONGBLOB NOT NULL, evidence_ref VARCHAR(128) NOT NULL, created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,adjustment_id), UNIQUE KEY adjustment_revision(tenant_id,app_id,original_task_id,revision)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS ledger_recoveries (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, recovery_id VARCHAR(128) NOT NULL,
 debt_id CHAR(64) NOT NULL, method VARCHAR(32) NOT NULL, amount DECIMAL(38,0) NOT NULL,
 evidence_ref VARCHAR(128) NOT NULL, created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,recovery_id), UNIQUE KEY evidence_once(tenant_id,app_id,method,evidence_ref)
) ENGINE=InnoDB;
