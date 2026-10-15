CREATE TABLE IF NOT EXISTS ledger_journal_links (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, journal_id CHAR(64) NOT NULL, entitlement_id CHAR(64) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,journal_id)
) ENGINE=InnoDB;
INSERT IGNORE INTO ledger_journal_links SELECT j.tenant_id,j.app_id,j.journal_id,e.entitlement_id FROM ledger_journals j JOIN ledger_entitlements e ON e.tenant_id=j.tenant_id AND e.app_id=j.app_id AND e.entitlement_id=j.journal_id;
CREATE TABLE IF NOT EXISTS reporting_exports (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, export_id VARCHAR(128) NOT NULL,
 subject_id VARCHAR(128) NOT NULL, kind VARCHAR(32) NOT NULL, filters LONGBLOB NOT NULL,
 snapshot LONGBLOB NOT NULL, csv_body LONGBLOB NOT NULL, snapshot_hash CHAR(64) NOT NULL,
 created_at DATETIME(6) NOT NULL, expires_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,export_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS platform_catalog_versions (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, catalog_type VARCHAR(32) NOT NULL,
 version_id VARCHAR(128) NOT NULL, effective_from DATETIME(6) NOT NULL, body LONGBLOB NOT NULL,
 PRIMARY KEY(tenant_id,app_id,catalog_type,version_id), UNIQUE KEY effective_version(tenant_id,app_id,catalog_type,effective_from)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS platform_component_states (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, component_type VARCHAR(32) NOT NULL,
 component_id VARCHAR(128) NOT NULL, enabled BOOLEAN NOT NULL, revision BIGINT UNSIGNED NOT NULL DEFAULT 0,
 PRIMARY KEY(tenant_id,app_id,component_type,component_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS platform_notifications (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, event_id VARCHAR(128) NOT NULL,
 topic VARCHAR(128) NOT NULL, resource_ref VARCHAR(128) NOT NULL, payload_hash CHAR(64) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,event_id)
) ENGINE=InnoDB;
