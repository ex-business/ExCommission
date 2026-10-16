-- 日终统计为独立扩展，快照包含发布版本、时区、层级、来源水位及各代理等级；不改历史等级表。
CREATE TABLE IF NOT EXISTS dayend_snapshots (
 tenant_id VARCHAR(128) NOT NULL, app_id VARCHAR(128) NOT NULL,
 plan_id VARCHAR(128) NOT NULL, business_day CHAR(10) NOT NULL,
 snapshot_json JSON NULL,
 PRIMARY KEY(tenant_id,app_id,plan_id,business_day)
) ENGINE=InnoDB;
