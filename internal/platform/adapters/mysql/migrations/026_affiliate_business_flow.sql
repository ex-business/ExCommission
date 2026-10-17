-- 独立代理业务编排表；不覆盖旧 qualification/referral/dayend 的规则、权益或历史。
CREATE TABLE IF NOT EXISTS affiliate_flow_spaces (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL,
 PRIMARY KEY(tenant_id,app_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliate_business_facts (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL,
 source_id VARCHAR(128) NOT NULL,event_id VARCHAR(128) NOT NULL,business_id VARCHAR(128) NOT NULL,kind VARCHAR(32) NOT NULL,user_id VARCHAR(128) NOT NULL,
 facts_hash CHAR(64) NOT NULL,body JSON NOT NULL,business_time DATETIME(6) NOT NULL,received_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,source_id,kind,business_id),KEY affiliate_member_facts(tenant_id,app_id,user_id,business_time),KEY affiliate_facts_day(tenant_id,app_id,business_time)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliate_agent_gates (
 tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,user_id VARCHAR(128) NOT NULL,
 revision BIGINT UNSIGNED NOT NULL,body JSON NOT NULL,
 PRIMARY KEY(tenant_id,app_id,user_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliate_reward_progress (
 tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,user_id VARCHAR(128) NOT NULL,
 body JSON NOT NULL,updated_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,user_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliate_business_rewards (
 tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,reward_id CHAR(64) NOT NULL,
 user_id VARCHAR(128) NOT NULL,agent_id VARCHAR(128) NOT NULL,kind VARCHAR(16) NOT NULL,
 status VARCHAR(32) NOT NULL,currency VARCHAR(16) NOT NULL,amount DECIMAL(38,0) NOT NULL,
 unlocked_at DATETIME(6) NOT NULL,claimed_at DATETIME(6) NULL,revision BIGINT UNSIGNED NOT NULL,body JSON NOT NULL,
 PRIMARY KEY(tenant_id,app_id,reward_id),KEY affiliate_member_rewards(tenant_id,app_id,user_id,kind,status),KEY affiliate_reward_records(tenant_id,app_id,kind,unlocked_at,reward_id),CHECK(amount>0)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliate_gate_history (
 tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,user_id VARCHAR(128) NOT NULL,
 revision BIGINT UNSIGNED NOT NULL,body JSON NOT NULL,occurred_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,user_id,revision)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS affiliate_business_events (
 tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,source_id VARCHAR(128) NOT NULL,event_id VARCHAR(128) NOT NULL,facts_hash CHAR(64) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,source_id,event_id)
) ENGINE=InnoDB;
