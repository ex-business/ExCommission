CREATE TABLE IF NOT EXISTS milestone_programs (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, program_id VARCHAR(128) NOT NULL,
 config LONGBLOB NOT NULL, config_hash CHAR(64) NOT NULL, approval_id VARCHAR(128) NOT NULL, activated_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,program_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS milestone_members (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, program_id VARCHAR(128) NOT NULL, user_id VARCHAR(128) NOT NULL,
 progress LONGBLOB NOT NULL, task_id VARCHAR(128) NULL, updated_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,program_id,user_id), UNIQUE KEY one_reward_task(tenant_id,app_id,task_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS milestone_observations (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, source_id VARCHAR(128) NOT NULL, source_event_id VARCHAR(128) NOT NULL,
 payload_hash CHAR(64) NOT NULL, canonical_payload LONGBLOB NOT NULL, response_body LONGBLOB NOT NULL, resource_id VARCHAR(128) NOT NULL, accepted_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,source_id,source_event_id)
) ENGINE=InnoDB;
