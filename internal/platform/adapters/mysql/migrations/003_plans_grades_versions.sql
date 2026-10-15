CREATE TABLE IF NOT EXISTS plan_drafts (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, plan_id VARCHAR(128) NOT NULL,
 revision BIGINT UNSIGNED NOT NULL, status VARCHAR(32) NOT NULL, config LONGBLOB NOT NULL,
 config_hash CHAR(64) NOT NULL, created_at DATETIME(6) NOT NULL, updated_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,plan_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS plan_versions (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, plan_id VARCHAR(128) NOT NULL,
 version_id VARCHAR(128) NOT NULL, version_number BIGINT UNSIGNED NOT NULL,
 config LONGBLOB NOT NULL, config_hash CHAR(64) NOT NULL, product_code VARCHAR(128) NOT NULL,
 event_type VARCHAR(32) NOT NULL, scene VARCHAR(128) NOT NULL,
 effective_from DATETIME(6) NOT NULL, effective_to DATETIME(6) NULL,
 created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,plan_id,version_id), UNIQUE KEY version_identity(tenant_id,app_id,version_id), UNIQUE KEY plan_number(tenant_id,app_id,plan_id,version_number),
 KEY route_time(tenant_id,app_id,product_code,event_type,scene,effective_from)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS plan_route_spaces (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL,
 PRIMARY KEY(tenant_id,app_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS plan_simulations (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, simulation_id VARCHAR(128) NOT NULL,
 plan_id VARCHAR(128) NOT NULL, config_hash CHAR(64) NOT NULL, snapshot_hash CHAR(64) NOT NULL,
 snapshot LONGBLOB NOT NULL, result LONGBLOB NOT NULL, passed BOOLEAN NOT NULL, created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,simulation_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS agent_plan_states (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, agent_id VARCHAR(128) NOT NULL,
 plan_id VARCHAR(128) NOT NULL, revision BIGINT UNSIGNED NOT NULL,
 PRIMARY KEY(tenant_id,app_id,agent_id,plan_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS agent_grade_history (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, agent_id VARCHAR(128) NOT NULL,
 plan_id VARCHAR(128) NOT NULL, grade_id VARCHAR(128) NOT NULL, eligibility VARCHAR(32) NOT NULL,
 source_kind VARCHAR(32) NOT NULL, evidence_ref VARCHAR(128) NOT NULL,
 effective_from DATETIME(6) NOT NULL, effective_to DATETIME(6) NULL,
 PRIMARY KEY(tenant_id,app_id,agent_id,plan_id,effective_from)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS grade_evaluations (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, evaluation_id VARCHAR(128) NOT NULL,
 plan_id VARCHAR(128) NOT NULL, calendar_period VARCHAR(128) NOT NULL,
 cutoff DATETIME(6) NOT NULL, source_watermark VARCHAR(128) NOT NULL,
 metrics LONGBLOB NOT NULL, results LONGBLOB NOT NULL, effective_from DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,evaluation_id), UNIQUE KEY daily_evaluation(tenant_id,app_id,plan_id,effective_from)
) ENGINE=InnoDB;
