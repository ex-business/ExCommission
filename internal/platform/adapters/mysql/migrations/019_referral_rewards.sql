-- 推荐奖励只拥有规则、解锁与领取进度；资金仅经ledger端口入账。
CREATE TABLE IF NOT EXISTS referral_reward_rules (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL,
 rule_id VARCHAR(128) NOT NULL, status VARCHAR(32) NOT NULL, rule_json JSON NOT NULL,
 PRIMARY KEY(tenant_id,app_id,rule_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS referral_reward_progress (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL,
 rule_id VARCHAR(128) NOT NULL, agent_id VARCHAR(128) NOT NULL, progress_json JSON NULL,
 PRIMARY KEY(tenant_id,app_id,rule_id,agent_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS referral_reward_inbox (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL, event_id VARCHAR(128) NOT NULL,
 body JSON NOT NULL, observed_at DATETIME(6) NOT NULL, status VARCHAR(16) NOT NULL DEFAULT 'PENDING',
 attempts INT NOT NULL DEFAULT 0, next_attempt_at DATETIME(6) NOT NULL, last_error VARCHAR(128) NULL,
 PRIMARY KEY(tenant_id,app_id,event_id)
) ENGINE=InnoDB;
