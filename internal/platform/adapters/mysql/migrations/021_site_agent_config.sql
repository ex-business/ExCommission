CREATE TABLE IF NOT EXISTS site_agent_states (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, revision BIGINT UNSIGNED NOT NULL,
 body LONGBLOB NULL, PRIMARY KEY(tenant_id,app_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS site_agent_versions (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, revision BIGINT UNSIGNED NOT NULL,
 effective_from DATETIME(6) NOT NULL, body LONGBLOB NOT NULL, approval_id VARCHAR(128) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,revision), KEY site_history(tenant_id,app_id,effective_from)
) ENGINE=InnoDB;
