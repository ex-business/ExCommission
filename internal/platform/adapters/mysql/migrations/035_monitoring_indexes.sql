-- 监控仅增加按站点和状态/请求编号检索的索引，不修改业务状态或历史权益。
ALTER TABLE audit_entries ADD KEY monitor_request (tenant_id,app_id,request_id,occurred_at);
ALTER TABLE payment_orders ADD KEY monitor_payment_state (tenant_id,app_id,status,updated_at);
ALTER TABLE payment_orders ADD KEY monitor_reconciliation (tenant_id,app_id,reconciliation_status,updated_at);
ALTER TABLE calculation_tasks ADD KEY monitor_task_state (tenant_id,app_id,status,created_at);
