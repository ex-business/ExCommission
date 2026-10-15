CREATE TABLE IF NOT EXISTS command_results (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL,
 operation VARCHAR(128) NOT NULL, idempotency_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 canonical_body LONGBLOB NOT NULL, body_hash BINARY(32) NOT NULL,
 http_status SMALLINT NULL, result_body LONGBLOB NULL, resource_id VARCHAR(128) NULL,
 created_at DATETIME(6) NOT NULL,
 PRIMARY KEY (tenant_id,app_id,operation,idempotency_key)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS identity_grants (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, subject_id VARCHAR(128) NOT NULL,
 permission VARCHAR(128) NOT NULL,
 PRIMARY KEY (tenant_id,app_id,subject_id,permission)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS integration_inbox (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, source_id VARCHAR(128) NOT NULL,
 event_id VARCHAR(128) NOT NULL, business_id VARCHAR(128) NOT NULL, event_type VARCHAR(32) NOT NULL,
 canonical_body LONGBLOB NOT NULL, body_hash BINARY(32) NOT NULL, status VARCHAR(32) NOT NULL,
 received_at DATETIME(6) NOT NULL,
 PRIMARY KEY (tenant_id,app_id,source_id,event_id),
 UNIQUE KEY business_once (tenant_id,app_id,source_id,business_id,event_type)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS transaction_outbox (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, event_id VARCHAR(128) NOT NULL,
 topic VARCHAR(128) NOT NULL, payload LONGBLOB NOT NULL, created_at DATETIME(6) NOT NULL,
 delivered_at DATETIME(6) NULL, attempts INT UNSIGNED NOT NULL DEFAULT 0,
 PRIMARY KEY (tenant_id,app_id,event_id), KEY pending_delivery (delivered_at,created_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS audit_entries (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, entry_id VARCHAR(128) NOT NULL,
 subject_id VARCHAR(128) NOT NULL, request_id VARCHAR(128) NOT NULL, log_type VARCHAR(32) NOT NULL,
 action VARCHAR(128) NOT NULL, resource_ref VARCHAR(128) NOT NULL, summary JSON NOT NULL,
 occurred_at DATETIME(6) NOT NULL,
 PRIMARY KEY (tenant_id,app_id,entry_id), KEY audit_time (tenant_id,app_id,occurred_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS platform_revisions (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL,
 resource_type VARCHAR(128) NOT NULL, resource_id VARCHAR(128) NOT NULL,
 revision BIGINT UNSIGNED NOT NULL,
 PRIMARY KEY (tenant_id,app_id,resource_type,resource_id)
) ENGINE=InnoDB;
