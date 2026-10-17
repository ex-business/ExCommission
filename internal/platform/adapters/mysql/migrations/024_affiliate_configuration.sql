CREATE TABLE IF NOT EXISTS affiliate_spaces (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL,
 revision BIGINT UNSIGNED NOT NULL DEFAULT 0,
 PRIMARY KEY(tenant_id,app_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliate_config_versions (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, revision BIGINT UNSIGNED NOT NULL,
 effective_from DATETIME(6) NOT NULL, body LONGBLOB NOT NULL, approval_id VARCHAR(128) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,revision), KEY affiliate_config_time(tenant_id,app_id,effective_from)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliate_venue_types (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, venue_type_id VARCHAR(128) NOT NULL,
 name VARCHAR(128) NOT NULL, icon_url VARCHAR(1024) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,venue_type_id)
) ENGINE=InnoDB;
