CREATE TABLE IF NOT EXISTS integration_event_keys (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, source_id VARCHAR(128) NOT NULL,
 event_id VARCHAR(128) NOT NULL, business_id VARCHAR(128) NOT NULL, event_type VARCHAR(32) NOT NULL,
 canonical_body LONGBLOB NOT NULL, original_event_id VARCHAR(128) NULL,
 PRIMARY KEY(tenant_id,app_id,source_id,event_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS calculation_tasks (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, task_id VARCHAR(128) NOT NULL,
 event_resource_id VARCHAR(128) NOT NULL, source_id VARCHAR(128) NOT NULL, source_event_id VARCHAR(128) NOT NULL,
 business_id VARCHAR(128) NOT NULL, event_type VARCHAR(32) NOT NULL, business_time DATETIME(6) NOT NULL,
 event_body LONGBLOB NOT NULL, status VARCHAR(32) NOT NULL, reason_code VARCHAR(128) NULL,
 snapshot LONGBLOB NULL, snapshot_hash CHAR(64) NULL, result LONGBLOB NULL,
 revision BIGINT UNSIGNED NOT NULL DEFAULT 0, fencing_token BIGINT UNSIGNED NOT NULL DEFAULT 0,
 lease_until DATETIME(6) NULL, retry_count INT UNSIGNED NOT NULL DEFAULT 0,
 next_attempt_at DATETIME(6) NULL, created_at DATETIME(6) NOT NULL, updated_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,task_id), UNIQUE KEY event_resource(tenant_id,app_id,event_resource_id),
 UNIQUE KEY original_event_task(tenant_id,app_id,source_id,source_event_id), KEY runnable(status,next_attempt_at,lease_until)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS integration_raw_payloads (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, source_id VARCHAR(128) NOT NULL, source_event_id VARCHAR(128) NOT NULL,
 raw_body LONGBLOB NOT NULL, raw_sha256 CHAR(64) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,source_id,source_event_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS user_agent_bindings (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, user_id VARCHAR(128) NOT NULL,
 agent_id VARCHAR(128) NOT NULL, effective_from DATETIME(6) NOT NULL, effective_to DATETIME(6) NULL,
 evidence_ref VARCHAR(128) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,user_id,effective_from)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS calculation_recoveries (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, task_id VARCHAR(128) NOT NULL,
 revision BIGINT UNSIGNED NOT NULL, evidence_ref VARCHAR(128) NOT NULL, resumed_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,task_id,revision)
) ENGINE=InnoDB;
