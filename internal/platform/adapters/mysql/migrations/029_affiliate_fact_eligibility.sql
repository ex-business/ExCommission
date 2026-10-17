-- 冻结业务发生时的站点参与资格；关闭期间事实仍保留，但重新开启不得把该期间补算为奖励。
ALTER TABLE affiliate_business_facts ADD COLUMN reward_eligible BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE affiliate_late_business_facts ADD COLUMN reward_eligible BOOLEAN NOT NULL DEFAULT TRUE;
