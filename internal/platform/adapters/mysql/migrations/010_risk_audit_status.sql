CREATE TABLE IF NOT EXISTS agent_status_history (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, agent_id VARCHAR(128) NOT NULL,
 status VARCHAR(32) NOT NULL, effective_from DATETIME(6) NOT NULL, effective_to DATETIME(6) NULL,
 evidence_ref VARCHAR(128) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,agent_id,effective_from)
) ENGINE=InnoDB;
INSERT IGNORE INTO agent_status_history
 SELECT tenant_id,app_id,agent_id,status,created_at,NULL,'initial-status-migration' FROM agent_accounts;
CREATE TABLE IF NOT EXISTS risk_requests (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, risk_id VARCHAR(128) NOT NULL,
 entitlement_id CHAR(64) NOT NULL, reason_code VARCHAR(128) NOT NULL, evidence_ref VARCHAR(128) NOT NULL,
 revision BIGINT UNSIGNED NOT NULL DEFAULT 0, status VARCHAR(32) NOT NULL, created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,risk_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS risk_appeals (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, appeal_id VARCHAR(128) NOT NULL,
 risk_id VARCHAR(128) NOT NULL, applicant_id VARCHAR(128) NOT NULL, reason VARCHAR(128) NOT NULL,
 evidence_ref VARCHAR(128) NOT NULL, created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,appeal_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS audit_security_states (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, event_id VARCHAR(128) NOT NULL,
 status VARCHAR(32) NOT NULL, revision BIGINT UNSIGNED NOT NULL,
 PRIMARY KEY(tenant_id,app_id,event_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS audit_handling_records (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, record_id VARCHAR(128) NOT NULL,
 event_id VARCHAR(128) NOT NULL, subject_id VARCHAR(128) NOT NULL, status VARCHAR(32) NOT NULL,
 reason VARCHAR(128) NOT NULL, occurred_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,record_id)
) ENGINE=InnoDB;
