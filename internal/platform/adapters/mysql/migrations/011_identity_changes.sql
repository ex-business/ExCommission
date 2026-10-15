CREATE TABLE IF NOT EXISTS identity_subject_revisions (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, subject_id VARCHAR(128) NOT NULL,
 revision BIGINT UNSIGNED NOT NULL DEFAULT 0,
 PRIMARY KEY(tenant_id,app_id,subject_id)
) ENGINE=InnoDB;
