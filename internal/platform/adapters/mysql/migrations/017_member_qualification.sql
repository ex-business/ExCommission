CREATE TABLE IF NOT EXISTS qualification_policies (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, policy_id VARCHAR(128) NOT NULL,
 configuration JSON NOT NULL, created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,policy_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS qualification_states (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, policy_id VARCHAR(128) NOT NULL,
 source_id VARCHAR(128) NOT NULL, external_user_id VARCHAR(128) NOT NULL,
 state_json JSON NULL, revision BIGINT UNSIGNED NOT NULL DEFAULT 0,
 PRIMARY KEY(tenant_id,app_id,policy_id,source_id,external_user_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS qualification_facts (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, policy_id VARCHAR(128) NOT NULL,
 source_id VARCHAR(128) NOT NULL, event_id VARCHAR(128) NOT NULL,
 event_type VARCHAR(32) NOT NULL, business_id VARCHAR(128) NOT NULL,
 external_user_id VARCHAR(128) NOT NULL, facts_hash CHAR(64) NOT NULL,
 fact_json JSON NOT NULL, business_time DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,policy_id,source_id,event_type,business_id),
 KEY qualification_member(tenant_id,app_id,policy_id,source_id,external_user_id,business_time)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS qualification_event_keys (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, source_id VARCHAR(128) NOT NULL,
 event_id VARCHAR(128) NOT NULL, facts_hash CHAR(64) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,source_id,event_id)
) ENGINE=InnoDB;
