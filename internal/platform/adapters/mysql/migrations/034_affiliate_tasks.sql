-- 运行记录和资金事务分开保存；租约带令牌围栏，重试仍执行原方案、原日期和原幂等业务键。
CREATE TABLE affiliate_task_runs (
 tenant_id VARCHAR(64) NOT NULL,app_id VARCHAR(64) NOT NULL,task_id CHAR(64) NOT NULL,
 program_id VARCHAR(64) NOT NULL,program_revision BIGINT UNSIGNED NOT NULL,business_day DATE NOT NULL,
 status VARCHAR(24) NOT NULL,retry_requested BOOLEAN NOT NULL DEFAULT FALSE,
 lease_token VARCHAR(64) NOT NULL DEFAULT '',lease_until DATETIME(6) NULL,updated_at DATETIME(6) NOT NULL,body JSON NOT NULL,
 PRIMARY KEY(tenant_id,app_id,task_id),UNIQUE KEY task_day(tenant_id,app_id,program_id,program_revision,business_day),KEY task_status(tenant_id,app_id,status,updated_at)
);
CREATE TABLE affiliate_task_attempts (
 tenant_id VARCHAR(64) NOT NULL,app_id VARCHAR(64) NOT NULL,task_id CHAR(64) NOT NULL,attempt INT NOT NULL,body JSON NOT NULL,
 PRIMARY KEY(tenant_id,app_id,task_id,attempt)
);
CREATE TABLE affiliate_task_alert_rules (
 tenant_id VARCHAR(64) NOT NULL,app_id VARCHAR(64) NOT NULL,revision BIGINT UNSIGNED NOT NULL,body JSON NOT NULL,
 PRIMARY KEY(tenant_id,app_id,revision)
);
CREATE TABLE affiliate_task_alerts (
 tenant_id VARCHAR(64) NOT NULL,app_id VARCHAR(64) NOT NULL,task_id CHAR(64) NOT NULL,kind VARCHAR(24) NOT NULL,body JSON NOT NULL,
 PRIMARY KEY(tenant_id,app_id,task_id,kind)
);
CREATE TABLE affiliate_task_alert_events (
 tenant_id VARCHAR(64) NOT NULL,app_id VARCHAR(64) NOT NULL,event_id CHAR(64) NOT NULL,task_id CHAR(64) NOT NULL,occurred_at DATETIME(6) NOT NULL,body JSON NOT NULL,
 PRIMARY KEY(tenant_id,app_id,event_id),KEY task_alert_history(tenant_id,app_id,occurred_at)
);
