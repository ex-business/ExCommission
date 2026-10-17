-- 查询快照只承载业务读模型，按作用域和操作者绑定，翻页/导出不重算变化中的数据。
CREATE TABLE IF NOT EXISTS affiliate_report_snapshots (
 tenant_id VARCHAR(128) NOT NULL,app_id VARCHAR(128) NOT NULL,snapshot_id CHAR(64) NOT NULL,
 subject_id VARCHAR(128) NOT NULL,body JSON NOT NULL,created_at DATETIME(6) NOT NULL,expires_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,app_id,snapshot_id),KEY affiliate_report_expiry(tenant_id,app_id,expires_at)
) ENGINE=InnoDB;
