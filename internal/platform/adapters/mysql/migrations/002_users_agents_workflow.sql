CREATE TABLE IF NOT EXISTS users_external (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, user_id VARCHAR(128) NOT NULL,
 source_id VARCHAR(128) NOT NULL, external_user_id VARCHAR(128) NOT NULL, display_name VARCHAR(128) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,user_id), UNIQUE KEY external_identity(tenant_id,app_id,source_id,external_user_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS user_sync_jobs (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, job_id VARCHAR(128) NOT NULL,
 source_id VARCHAR(128) NOT NULL, mapping_version_id VARCHAR(128) NOT NULL, status VARCHAR(32) NOT NULL,
 watermark VARCHAR(128) NULL, success_count INT UNSIGNED NOT NULL, failure_count INT UNSIGNED NOT NULL,
 error_refs JSON NOT NULL, created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,job_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS agent_accounts (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, agent_id VARCHAR(128) NOT NULL,
 user_id VARCHAR(128) NOT NULL, display_name VARCHAR(128) NOT NULL, status VARCHAR(32) NOT NULL,
 revision BIGINT UNSIGNED NOT NULL, created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,agent_id), UNIQUE KEY user_agent(tenant_id,app_id,user_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS agent_relation_spaces (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL,
 PRIMARY KEY(tenant_id,app_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS agent_relations (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, agent_id VARCHAR(128) NOT NULL,
 parent_id VARCHAR(128) NULL, effective_from DATETIME(6) NOT NULL, effective_to DATETIME(6) NULL,
 PRIMARY KEY(tenant_id,app_id,agent_id,effective_from), KEY parent_history(tenant_id,app_id,parent_id,effective_from)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS agent_contracts (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, contract_id VARCHAR(128) NOT NULL,
 agent_id VARCHAR(128) NOT NULL, plan_id VARCHAR(128) NOT NULL, effective_from DATETIME(6) NOT NULL,
 effective_to DATETIME(6) NULL, evidence_ref VARCHAR(128) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,contract_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS workflow_requests (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, approval_id VARCHAR(128) NOT NULL,
 request_type VARCHAR(64) NOT NULL, applicant_id VARCHAR(128) NOT NULL, object_ref VARCHAR(128) NOT NULL,
 object_hash CHAR(64) NOT NULL, payload JSON NOT NULL, revision BIGINT UNSIGNED NOT NULL,
 status VARCHAR(32) NOT NULL, execution_status VARCHAR(32) NOT NULL, decision_by VARCHAR(128) NULL,
 reason VARCHAR(512) NOT NULL, decision_reason VARCHAR(512) NULL, created_at DATETIME(6) NOT NULL, decided_at DATETIME(6) NULL,
 PRIMARY KEY(tenant_id,app_id,approval_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS identity_bootstraps (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, subject_id VARCHAR(128) NOT NULL,
 initialized_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,subject_id)
) ENGINE=InnoDB;
