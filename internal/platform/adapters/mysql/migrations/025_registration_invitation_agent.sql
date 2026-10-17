-- 统一分享凭证按代理读取，避免每次会员分享扫描同来源全部邀请码；旧码与归属数据保持不变。
ALTER TABLE registration_invitations ADD KEY invitation_agent (tenant_id,app_id,source_id,agent_id,created_at,invitation_code);
