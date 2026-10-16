# 推荐奖励：直属有效人数阶梯

目录：[规则与边界](#scope) · [配置与启用](#config) · [接口](#api) · [进度与领取](#flow) · [幂等和资金](#money) · [验证与扩展](#verification)

<a id="scope"></a>
## 规则与边界

推荐奖励归属独立 `referral` 业务模块，引用有效会员统计，与三层有效业务量返佣分开。每个代理在同一奖励规则下，每个阶梯只奖励一次，不按日、周、月重置领取记录。

- 直属人数按注册时保存的**首次邀请人**筛选，不计二级下线，不因后续关系迁移转给新上级。
- 只有已同时满足充值及有效业务量门槛的受邀会员才计入人数，使用首次真实业务达标时间。
- 人数的自然日／周／月／永久范围与时区，由引用的不可变有效会员规则定义，见[统计范围](member-qualification-integration.md#member-statistics)。累计金额不会因为人数周期重置而清零。
- 每阶金额是该阶独立奖励，例如第一阶500、第二阶800，两阶都领共1300最小货币单位；不是补差额。达标时可同时解锁多阶，不要求顺序领取。
- 解锁后保存人数、周期与排序后的会员集合摘要；周期切换、迟到更正不会自动回锁或抹掉已领记录。当前不提供奖励自动冲正接口。

当前是受控管理端／服务端接入能力，沿用现有 Bearer 权限和租户隔离。请求中的 `agent_id` 不是会员身份凭证；实际 C 端接入时，必须由已登录会员身份在服务端解析自己的代理 ID，不能直接把管理员凭据交给浏览器。

<a id="config"></a>
## 配置与启用

API 与 worker 均设置 `LOCAL_REFERRAL_REWARDS_ENABLED=true`，默认关闭。Compose 已透传；来源事实接口仍需要原注册／有效会员模块的启用及密钥配置。

创建不可变奖励规则：

```json
{
  "rule_id": "direct-invite-v1",
  "name": "直属有效会员奖励",
  "plan_id": "published-plan",
  "source_id": "sandbox",
  "qualification_policy_id": "effective-policy",
  "currency": "USD",
  "tiers": [
    {"tier_id": "tier-1", "required_direct_members": 1, "amount_minor": "500"},
    {"tier_id": "tier-2", "required_direct_members": 3, "amount_minor": "800"}
  ]
}
```

| 字段 | 配置决策 |
| --- | --- |
| rule_id | 稳定奖励规则编号；同编号内容不能覆盖。新编号代表另一套可重新领奖的活动，必须经过独立启用复核，不用于绕过原规则的一次性限制。 |
| plan_id | 已发布方案，提供代理参与资格、范围、币种和结算策略，并保留财务报表归属；不使用其业务量返佣费率计算推荐奖励。 |
| qualification_policy_id | 已存在的有效会员规则；来源和币种必须一致。其统计范围创建后不可改。 |
| currency | 奖励币种，必须与引用方案和有效会员规则一致，并存在于当前货币目录。 |
| required_direct_members | 阶梯人数，正整数，上限十亿；各阶人数不得重复。 |
| amount_minor | 正整数最小货币单位，最多38位；USD 的500表示5美元。不接受0、负数、浮点或前导零。 |

最多64阶，阶梯ID不可重复。创建后为 `PENDING_APPROVAL`，独立审核人核对 `config_hash` 后才能启用；申请人不能自审。拒绝后为 `REJECTED`，不能用新幂等键偷偷启用。原定义、审核人和原因保留。需要不同金额、门槛或规则引用时创建新编号，不覆盖历史。

<a id="api"></a>
## 接口

所有写入需要 `Idempotency-Key`；响应沿用 `{data,request_id}`。权限由现有身份系统提供，不新增公共鉴权逻辑。

| 方法与路径 | 权限 | 用途 |
| --- | --- | --- |
| POST `/v1/referral-reward-rules` | plans:write | 提交上述不可变配置，返回规则及配置摘要。 |
| GET `/v1/referral-reward-rules/{id}` | plans:read | 读取配置、状态、创建人和复核记录。 |
| POST `/v1/referral-reward-rules/{id}/activation` | plans:approve | 独立复核启用或拒绝，不能自审。 |
| GET `/v1/referral-reward-rules/{id}/progress?agent_id=...` | agents:read | 读取已保存进度和每阶状态；`refreshed_at` 为null代表尚未刷新。 |
| POST `/v1/referral-reward-rules/{id}/progress-refresh` | agents:write | 显式刷新当前人数与解锁，body为 `{"agent_id":"..."}`；后续想取新结果需用新幂等键。 |
| POST `/v1/referral-reward-claims` | agents:write | 领取某阶，内部也会刷新当前人数。 |
| GET `/v1/referral-reward-claims/{id}` | agents:read | 按claim_id读取原领取凭证，包含资金权益、币种、方案版本及规则摘要。 |

启用／拒绝请求：

```json
{
  "expected_config_hash": "创建响应中的config_hash",
  "decision": "APPROVE",
  "reason": "已核对直属人数阶梯和奖励金额"
}
```

拒绝时 `decision=REJECT`。这是推荐奖励自己的启用入口，沿用方案审批权限及双人复核原则，不向原通用审批类型分发中插入新的公共鉴权分支。

领取请求：

```json
{"rule_id":"direct-invite-v1","agent_id":"beneficiary-agent","tier_id":"tier-1"}
```

返回奖励状态、`claim_id`、`entitlement_id`、`claimed_at` 和原解锁证据。领取成功表示进入待结算账簿，不表示外部打款成功。

<a id="flow"></a>
## 进度与领取

| 阶梯状态 | 含义与下一步 |
| --- | --- |
| LOCKED | 尚未观测到满足该阶人数；可查询或刷新进度。 |
| CLAIMABLE | 已解锁，证据已保存；即使下一周期人数变零，仍可领取。 |
| CLAIMED | 已领取，返回原凭证；后续重复请求不会产生新奖励。 |

领取前还会检查：规则已独立启用、当前仍有有效发布方案、代理处于ACTIVE且具备该方案已审批的参与资格、属于方案范围；方案要求合同时还必须有有效合同。冻结、停用、未获资格不能新领奖励，不自动赋予资格。已领请求的重放只返回原凭证。

```text
Registration invitation -> original direct inviter
                  |
                  v
     Qualification successful facts
                  |
                  v
       Qualification transaction outbox
                  |
                  v
   Referral inbox -> retry / progress refresh
                  |
                  v
       LOCKED -> CLAIMABLE -> CLAIMED
                  |               |
       Persist unlock evidence    v
                           Existing pending ledger
                                  |
                                  v
                       Existing statement approval
                                  |
                                  v
                         Existing payment workflow
```

worker 从原有效会员outbox可靠转存到推荐奖励自己的inbox，再独立处理和退避重试。奖励业务处理失败不会阻塞原审计／通知outbox。对跨周期后才消费的旧outbox事件，使用原事件处理时点统计，保留已解锁记录，旧事件不回退当前进度。

后台还有周期刷新和规则启用时点回补；当前人数轮询按分钟复用操作键。显式刷新及领取会立即重算，不必等待轮询。GET只读取保存结果，不通过读取请求偷偷触发写入。

目前不自动重算“新补录历史业务事实”所涉及的已结束人数周期，也不重放启用前已结束周期的活动奖励。真实来源应按既有规则可靠投递事实；该边界不同于已产生outbox的延迟消费。

<a id="money"></a>
## 幂等和资金

同一规则、代理、阶梯使用稳定claim_id；数据库另有该业务组合唯一约束。更换投递键、并发领取、切换人数周期或更新已发布方案版本，都不能重复领取同一阶。

进度锁串行同一代理与规则的领取。账簿入账、独立领取凭证、进度和奖励领取outbox在同一事务内提交，任一步失败全回滚。基础设施错误后重试原请求，不应自行生成另一条业务奖励记录。

推荐奖励通过ledger扩展端口复用平衡分录、待结算余额和支付策略；`event_type=REFERRAL_REWARD`，真实 `plan_version_id` 关联财务报表；此类权益的 `task_id` 引用领取资源，不是业务量计算任务。账簿沿用现有佣金费用科目；推荐奖励可通过事件类型及规则追溯，不伪造业务量计算快照。

| 结果 | 处理 |
| --- | --- |
| 200 + CLAIMED | 已领或此次领取成功，使用原claim_id与entitlement_id继续查询。 |
| 409 | 规则配置不可覆盖、审批摘要过期、规则未启用或代理状态冲突，核对原对象，不创建重复活动规避限制。 |
| 422 / EVIDENCE_NOT_CONFIRMED | 人数未解锁、资格未获批准或合同缺失，补齐真实条件后用新操作键请求。 |
| 401 / 403 | 未认证或无权限，服务端修正身份配置。 |
| 基础设施故障 | 保留业务ID重试；原子事务不会留下已领却无权益的半条记录。 |

<a id="verification"></a>
## 验证与扩展

已验证真实MySQL下：注册邀请与双门槛输入、排除二级会员、双人启用及拒绝自审、8个并发请求只发一次、跨日人数归零但已解锁不丢失、旧outbox延迟消费、冻结与资格保护、写账后故障全回滚、独立领取凭证及租户隔离、坏事件退避不阻塞后续事件、旧财务报表与结算审批。

【涉及其他业务】有效会员模块增加原始直属邀请归属的只读筛选端口；代理模块增加领取资格锁定读取；ledger增加独立奖励入账端口；runtime连接独立任务和outbox接收；Compose显式透传推荐奖励与日终开关。原计佣算法、月度／日终定级策略、付款服务和公共base、鉴权、站点识别未修改。原财务报表和日结纳入奖励权益已回归；实际外部打款、C端身份映射、页面配置及领取按钮尚未接入。

新增持久化仅属于推荐奖励模块：规则、进度、inbox及领取凭证。统计、资格、账簿均经所属模块端口访问。当前后台回补扫描适用于本地联调；大规模接入应替换为批量预聚合及带游标的刷新，保持一次性领取和账簿端口不变。

本功能工作区包含前一未提交功能“有效会员四种统计范围”的依赖快照；原分支仍保留，未搬移或丢弃其改动。
