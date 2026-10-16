# 会员佣金领取与状态闭环

## 业务边界

新增可复用 MEMBER_CLAIM 派发模式，原 DIRECT 模式继续支持后台直接派发。模式在创建派发单时确定，不可修改；省略模式兼容 DIRECT。佣金审核仍由独立审核人完成，不允许会员代替审核。

会员领取模式：方案结算提交审核后为待审核；审核拒绝或撤销为取消派发；审核通过后由后台配置金额、渠道并发布领取单，发布前为准备中，发布后为待领取。会员领取单状态为 DRAFT → CLAIMABLE → QUEUED → LINKED；未发起领取的 CLAIMABLE 可取消。DRAFT、BLOCKED 也可取消，取消只关闭本次派发，不删除佣金权益。

会员发起领取后为领取处理中，现有付款模块负责预留、幂等、提交、查询和渠道回调。仅匹配金额、币种且确认成功的付款显示已领取。UNKNOWN 一直保留处理中，不创建新付款；终态失败显示领取失败，后台取消旧单后重新配置。对账异常单独显示核验异常，不伪装成功。部分领取的已领取只表示该派发单金额，不表示整张结算单已领完。

## 身份与授权

会员登录身份必须由服务端可信配置绑定内部 user_id；与 agent_accounts.user_id 匹配后只能读取、领取自己的佣金。请求体不接受会员、金额、渠道或收款人覆盖。新增 member:read、member:claim 独立持久化授权，每次请求及领取前重新检查，管理员权限不自动具有会员身份。

本地账号配置可显式为非 operator/approver 账号指定 member_user_id；subject 固定由账号名计算。第三方 introspection 配置新增 members={内部subject:内部user_id}，且 subject 必须以 member- 开头；bindings 仍显式绑定经发行方校验的外部 subject。仅配置的会员初始化会员权限，撤销后重启不恢复。配置为部署侧信任边界，禁止公开上传。修改绑定后应重启服务以失效旧本地会话。

## 接口

POST /v1/payout-orders 新增可选 delivery_mode=MEMBER_CLAIM|DIRECT。发布仍使用 RELEASE；DIRECT 进入付款队列，MEMBER_CLAIM 只进入 CLAIMABLE，后台任务不会提前付款。

GET /v1/me/commissions?after_id=&limit= 返回本人结算单、真实审核状态和各次派发的业务状态；分页按结算单 ID，每页最多100。会员模式派发单包含 instruction_id、金额、修订号、claim_status；不返回后台备注、审核人、渠道凭据。

POST /v1/me/commission-orders/{id}/claim，body={expected_revision}，必须携带 Idempotency-Key。先核验身份所有权，再读取幂等结果；同一单并发不同键仅一次入队，重复领取返回原单。重用键改变载荷冲突；领取处理中不能取消或改金额。领取前重验最新结算审批、余额、冻结、账号、渠道，付款创建时再次原子校验。

后台 payout-orders 返回 claim_status 投影；不将结算状态、付款状态和会员领取状态混成同一个可随意修改字段。原始状态用于运营恢复，claim_status 用于业务展示。

## 验收

覆盖待审核与拒绝、发布不付款、本人领取、跨会员/跨租户隔离、管理员不能冒领、并发和幂等、冻结或陈旧审批拒绝、UNKNOWN 不误报成功、成功重复回调只记账一次、失败和异常投影、取消后无法领取、旧直接派发兼容、撤权后缓存请求拒绝。会员界面属于独立前端项目。

## 本地与第三方接入配置

在已有 AUTH_LOCAL_ACCOUNTS_FILE 数组中增加会员项：username、salt（16字节十六进制）、hash（PBKDF2-HMAC-SHA256，600000次，32字节十六进制）、member_user_id（已同步的内部用户 ID）。禁止填写为 operator/approver；密码使用已有本地登录接口提交，会员权限自动做一次性初始化。不得在公开示例中填写真实密码或用户数据。

第三方配置示意（占位值需部署时替换）：

```json
{
  "endpoint": "https://identity.example/introspect",
  "client_id": "commission-client",
  "client_secret": "部署侧密钥",
  "issuer": "https://identity.example",
  "audience": "commission",
  "bindings": {"external-member-subject": "member-registered-subject"},
  "members": {"member-registered-subject": "内部用户ID"}
}
```

第三方调用方携带其 Bearer token，直接访问本人查询和领取接口；发行方响应需满足现有 active、iss、aud、exp 检查。网页会员入口使用本地账号密码登录；第三方网页单点登录跳转不在本次新增范围内，后端已提供可信令牌接入。

本次扩展沿用018中派发单 JSON 持久化和 active_slot 唯一约束，无需改写历史迁移；CLAIMABLE 同样占有 active_slot，避免同结算单同时发布多个待领取单。领取并发先锁派发单，付款模块再锁结算单与权益余额，保持原有资金守恒与稳定商户单号。
