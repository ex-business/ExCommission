CREATE TABLE IF NOT EXISTS registration_invitations (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL,
 source_id VARCHAR(128) NOT NULL, invitation_code VARCHAR(128) NOT NULL,
 agent_id VARCHAR(128) NOT NULL, created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,source_id,invitation_code)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS registration_receipts (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL,
 source_id VARCHAR(128) NOT NULL, external_user_id VARCHAR(128) NOT NULL,
 event_id VARCHAR(128) NOT NULL, facts_hash CHAR(64) NOT NULL,
 receipt JSON NOT NULL,
 PRIMARY KEY(tenant_id,app_id,source_id,external_user_id),
 UNIQUE KEY registration_event(tenant_id,app_id,source_id,event_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS registration_events (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL,
 source_id VARCHAR(128) NOT NULL,event_id VARCHAR(128) NOT NULL,facts_hash CHAR(64) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,source_id,event_id)
) ENGINE=InnoDB;
