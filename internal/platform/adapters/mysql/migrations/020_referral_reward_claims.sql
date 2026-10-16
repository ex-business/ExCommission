-- 独立领取凭证使用稳定业务唯一键，不按天/月或方案版本重置；与资金和进度同事务提交。
CREATE TABLE IF NOT EXISTS referral_reward_claims (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL,
 claim_id VARCHAR(128) NOT NULL, rule_id VARCHAR(128) NOT NULL,
 agent_id VARCHAR(128) NOT NULL, tier_id VARCHAR(128) NOT NULL,
 receipt_json JSON NOT NULL,
 PRIMARY KEY(tenant_id,app_id,claim_id),
 UNIQUE KEY referral_once(tenant_id,app_id,rule_id,agent_id,tier_id)
) ENGINE=InnoDB;
