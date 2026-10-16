# 简单登录与第三方身份对接

本次新增契约：后台操作身份与佣金业务用户是不同概念。同步业务用户不会授予后台登录权限。

本地：POST /v1/auth/login 接受 {"username":"operator","password":"..."}，返回 access_token、token_type=Bearer、expires_in=28800 和 principal（subject_id、tenant_id、app_id）。GET /v1/auth/me 使用 Bearer 返回当前主体、实时数据库权限与权限修订。POST /v1/auth/logout 撤销当前本地会话。GET /v1/auth/config 仅返回可用登录方式，不暴露凭据。

本地受控账号仅 operator 与 approver，映射 local-operator / local-approver。密码以 PBKDF2-HMAC-SHA256、600000轮、独立随机盐保存于忽略文件；没有公开注册、客户端角色指定或密码默认值。运行 python3 tools/init_login.py 初始化，密码仅保存在本地0600文件 .local/login-credentials.json，禁止复制进前端包。会话为随机256位opaque令牌，内存只保存摘要，8小时绝对过期；重启需重新登录，适合当前单实例。最多10000会话，登录限速每账号每分钟10次、全局每分钟60次（单进程，不使用可伪造代理头）；多实例上线需替换共享会话与限速存储。

第三方：配置服务端 AUTH_EXTERNAL_FILE 指向只读JSON，包含 endpoint（HTTPS RFC7662 introspection）、client_id、client_secret、issuer、audience、bindings（第三方sub到已授权本地主体的显式映射）。当前只允许映射本地已登记的操作员/审批员，不自动创建账号或授权。服务以HTTP Basic鉴权POST token和token_type_hint=access_token，每个请求重新验证 active=true、iss、aud、sub与exp（必填），校验超时3秒，不跟随重定向，不向客户端提供密钥。未知主体拒绝；上游失败返回503，失效令牌401。外部角色、租户与权限声明不作为本地授权依据。默认关闭，不产生虚构的第三方登录按钮。

第三方登录页/授权码与PKCE流程由现有平台或前端服务端适配器完成，平台返回的access_token由服务端保管并作为Bearer传入；本接口不声称实现各厂商授权页面。外部token退出时前端清除会话，第三方撤销由身份平台处理。外部token每次调用检查，不换成本地8小时会话，避免撤销后继续可用。

前端建议用服务端会话和HttpOnly Cookie代理保存后端令牌，浏览器不保存长期沙箱令牌、客户端密钥或日志中的密码。现有固定令牌暂保留兼容服务调用，不能做成免密身份切换按钮。登录接口不写密码/token到审计；受保护API继续审计并实时鉴权。源站无CORS开放，前端走同源服务端代理；生产要求HTTPS。

配置样例（无真实密钥）：
```json
{"endpoint":"https://identity.example.com/oauth/introspect","client_id":"commission","client_secret":"SERVER_ONLY","issuer":"https://identity.example.com","audience":"commission-api","bindings":{"external-operator":"local-operator","external-approver":"local-approver"}}
```

协议参考：https://www.rfc-editor.org/rfc/rfc7662.html
