-- 独立方案使用真实站点作用域，禁止用伪造站点或会员编号隔离方案。
CREATE TABLE affiliate_program_versions (
 tenant_id VARCHAR(64) NOT NULL, app_id VARCHAR(64) NOT NULL, program_id VARCHAR(64) NOT NULL,
 revision BIGINT UNSIGNED NOT NULL, effective_from DATETIME(6) NOT NULL, body JSON NOT NULL,
 PRIMARY KEY(tenant_id,app_id,program_id,revision), UNIQUE KEY program_effective(tenant_id,app_id,program_id,effective_from)
);
CREATE TABLE affiliate_program_receipts (
 tenant_id VARCHAR(64) NOT NULL, app_id VARCHAR(64) NOT NULL, source_id VARCHAR(64) NOT NULL,
 kind VARCHAR(32) NOT NULL, business_id VARCHAR(128) NOT NULL, body JSON NOT NULL,
 PRIMARY KEY(tenant_id,app_id,source_id,kind,business_id)
);
CREATE TABLE affiliate_program_facts (
 tenant_id VARCHAR(64) NOT NULL, app_id VARCHAR(64) NOT NULL, program_id VARCHAR(64) NOT NULL,
 revision BIGINT UNSIGNED NOT NULL, source_id VARCHAR(64) NOT NULL, kind VARCHAR(32) NOT NULL,
 business_id VARCHAR(128) NOT NULL, user_id VARCHAR(128) NOT NULL, business_time DATETIME(6) NOT NULL, body JSON NOT NULL,
 PRIMARY KEY(tenant_id,app_id,program_id,source_id,kind,business_id), KEY program_time(tenant_id,app_id,program_id,revision,business_time)
);
CREATE TABLE affiliate_program_days (
 tenant_id VARCHAR(64) NOT NULL, app_id VARCHAR(64) NOT NULL, program_id VARCHAR(64) NOT NULL,
 revision BIGINT UNSIGNED NOT NULL, business_day VARCHAR(10) NOT NULL, body JSON NOT NULL,
 PRIMARY KEY(tenant_id,app_id,program_id,revision,business_day)
);
