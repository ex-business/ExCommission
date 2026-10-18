-- 冲正单与原事实分开保存；原事件、奖励领取和钱包流水保持不可变历史。
CREATE TABLE IF NOT EXISTS affiliate_reversal_events (
 tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,source_id VARCHAR(128) NOT NULL,business_id VARCHAR(128) NOT NULL,
 event_id VARCHAR(128) NOT NULL,facts_hash CHAR(64) NOT NULL,body JSON NOT NULL,received_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,source_id,business_id),UNIQUE KEY reversal_event(tenant_id,app_id,source_id,event_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliate_reversal_totals (
 tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,source_id VARCHAR(128) NOT NULL,kind VARCHAR(32) NOT NULL,business_id VARCHAR(128) NOT NULL,
 amount DECIMAL(38,0) NOT NULL,valid_bet DECIMAL(38,0) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,source_id,kind,business_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliate_reversal_baselines (
 tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,reward_id CHAR(64) NOT NULL,body JSON NOT NULL,
 PRIMARY KEY(tenant_id,app_id,reward_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliate_reversal_adjustments (
 tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,adjustment_id CHAR(64) NOT NULL,reversal_id CHAR(64) NOT NULL,reward_id CHAR(64) NOT NULL,
 user_id VARCHAR(128) NOT NULL,currency VARCHAR(16) NOT NULL,before_target DECIMAL(38,0) NOT NULL,after_target DECIMAL(38,0) NOT NULL,debt_added DECIMAL(38,0) NOT NULL,status VARCHAR(32) NOT NULL,created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,adjustment_id),KEY reversal_record(tenant_id,app_id,reversal_id)
) ENGINE=InnoDB;
