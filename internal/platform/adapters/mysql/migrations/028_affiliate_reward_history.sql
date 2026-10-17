-- 每次业务状态流转保存不可变快照，分页及导出按观察时点读取，领取/审核不会改写已查询的历史。
CREATE TABLE IF NOT EXISTS affiliate_reward_history (
 tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,reward_id CHAR(64) NOT NULL,
 revision BIGINT UNSIGNED NOT NULL,sequence_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,recorded_at DATETIME(6) NOT NULL,body JSON NOT NULL,
 PRIMARY KEY(tenant_id,app_id,reward_id,revision),UNIQUE KEY affiliate_history_sequence(sequence_id),KEY affiliate_record_snapshot(tenant_id,app_id,recorded_at,reward_id)
) ENGINE=InnoDB;
