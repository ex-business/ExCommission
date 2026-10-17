CREATE TABLE IF NOT EXISTS member_referral_links (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, agent_id VARCHAR(128) NOT NULL,
 code VARCHAR(128) NOT NULL, created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,agent_id), UNIQUE KEY scoped_code(tenant_id,app_id,code)
) ENGINE=InnoDB;
