-- 来源日结、冻结计算和迟到调整队列独立持久化，重试不能重算已结业务日。
CREATE TABLE IF NOT EXISTS affiliate_source_watermarks (
 tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,source_id VARCHAR(128) NOT NULL,
 business_day CHAR(10) NOT NULL,body JSON NOT NULL,received_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,source_id,business_day)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliate_daily_runs (
 tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,business_day CHAR(10) NOT NULL,
 status VARCHAR(32) NOT NULL,body JSON NOT NULL,updated_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,business_day),KEY affiliate_unsettled_days(tenant_id,app_id,status,business_day)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliate_late_business_facts LIKE affiliate_business_facts;
