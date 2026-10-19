-- 代理领取的持久派发意图与权益预留在同一事务；原单编号跨超时和重启保持不变。
CREATE TABLE IF NOT EXISTS affiliate_wallet_deliveries (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL,
 delivery_id CHAR(64) NOT NULL, reward_id CHAR(64) NOT NULL, claim_id CHAR(64) NOT NULL,
 user_id VARCHAR(128) NOT NULL, status VARCHAR(32) NOT NULL,
 next_at DATETIME(6) NOT NULL, lease_until DATETIME(6) NOT NULL, lease_token CHAR(64) NOT NULL,
 body JSON NOT NULL, created_at DATETIME(6) NOT NULL, updated_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,delivery_id),
 UNIQUE KEY affiliate_claim_reward(tenant_id,app_id,reward_id,claim_id),
 KEY affiliate_wallet_due(tenant_id,app_id,status,next_at),
 KEY affiliate_wallet_member(tenant_id,app_id,user_id,created_at)
) ENGINE=InnoDB;
