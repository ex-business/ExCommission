# 累计达标与一次奖励扩展契约

目标：通过配置完成受邀会员充值与有效投注同时达标的一次性奖励，并可复用到其他累计指标。不是新增写死的充值/投注奖励算法。

## 配置

`POST /v1/milestone-program-requests` 提交 {program_id,name,source_id,starts_at,ends_at,conditions:[{metric,unit,threshold}],logic:"ALL",frequency:"ONCE_PER_SUBJECT",reward_plan_id,reason}。1至16个唯一指标，门槛必须大于零；非负18位整数及最多8位小数，无浮点。指标是来源定义的稳定编码（例如 CONFIRMED_DEPOSIT、VALID_TURNOVER），unit必须精确一致，不换汇、不把缺失当零。source_id是该活动唯一的权威来源。窗口为UTC半开区间，来源提供窗口内累计确认总量，不是逐笔增量。

申请人与批准人必须独立，复用审批流程，只有批准执行成功才可接收统计；批准时starts_at必须仍在未来。配置一经激活不可覆盖，活动ID不允许换名重新激活；调整门槛应建立新活动并重新审批。拒绝、撤回与失败恢复沿用统一审批机制。

reward_plan_id引用已发布的标准佣金方案。方案route.event_type必须为内部专用MILESTONE_REACHED，规则必须只有一条FIXED、DIRECT、无附加条件（空AND），以免一次触发因第二套条件变成未奖励；金额、币种、资格、预算、合同、冻结和结算仍由原方案控制。支持计划按既有审批发布后续版本，达标时按业务时点选择，保留历史方案快照；方案停用/不匹配时拒绝该次输入，不伪造成功，可换幂等键重试。它不直接打款，不绕过独立结算审批。

## 第三方累计事实

`POST /v1/milestone-observations` 携带Bearer、Idempotency-Key、X-Source-Signature（LOCAL_SOURCE_KEY对原始UTF-8 JSON作HMAC-SHA256十六进制签名）。数据：{source_id,event_id,program_id,external_user_id,beneficiary_agent_id,metric,unit,cumulative_value,observed_at,evidence_ref}。当前沙箱只登记sandbox来源，真实来源必须通过受控适配接入，不能靠客户端自报新source_id获得信任；来源停用后拒绝新事实。

来源必须已完成业务用户同步；beneficiary_agent_id由可信来源提供并签名，表示此活动会员绑定的奖励代理，第一条事实冻结此绑定；之后禁止换人（即使已触发）。不得是会员自己，代理必须在事实时点存在。此绑定属于活动，不覆盖全局代理关系，可用于受邀绑定或其他来源确认的奖励归属。证据与首次绑定时点保留，来源负责证明邀请关系及累计统计的真实性。

每个(租户,应用,活动,内部会员)保存独立进度；每指标保存累计高水位和事实时间。缺少任一条件不触发。较低累计值或更早时间的较小累计值只记录STALE_NOOP，不降低已确认总量；同一时刻不同值、时间更早却值更高拒绝冲突。相等累计值为NO_CHANGE。所有有效累计值只增不减；退款后净充值等可回退指标不支持此合同，不能把净值当单调累计上报。此版本不自动撤回已发权益，业务纠错须走已有独立审批更正/追回流程。

相同来源event_id内容一致重放返回原结果（不再触发），不同内容409；业务语义相等的小数写法和UTC微秒时间归一化。不同事件号重复累计也不会重复发奖。超过活动窗口或未来事实拒绝；窗口内迟到事实可在活动结束后补报。时间按各已确认指标最大事实时点确定，不能早于活动首次绑定。

## 原子触发与查询

活动会员进度行串行锁；全部条件首次满足时，在同一MySQL事务中保存达标证据、唯一奖励标记、原生计佣事件和持久任务。唯一约束是(租户,应用,活动,会员)，与重试次数、事件号、付款结果和方案后续版本无关。不同活动各自独立。

MILESTONE_REACHED只允许内部生成，外部普通/v1/events不能提交此类型或指定受益人。内部事件携带冻结的活动绑定，标准解析器按此绑定找DIRECT代理，仍校验历史代理状态和方案资格，拒绝自奖励。worker复用已有防重权益与复式账簿，重启/并发/重复来源消息只有原任务。任务未成功或NO_MATCH不是到账；冻结/缺失资格等按原任务查询、恢复，不创建第二个奖励。

`GET /v1/milestone-programs/{id}` 返回已批准配置、配置摘要与审批引用。`GET /v1/milestone-programs/{id}/members/{user_id}` 返回完整指标状态（缺失标记known=false）、绑定、达标时间、唯一task_id及实际task_status；触发前WAITING，触发后TRIGGERED，只有账簿/结算/支付状态可证明后续资金处理。

新增权限milestones:request、milestones:approve、milestones:write、milestones:read通过原有权限审批管理；本地只受控初始化操作员申请/写入/读取，审批员批准/读取。

验收：两个条件任意顺序、分次累计、恰好门槛/未达/缺失、重复不同事件号与并发100、签名/单位/时间/主体/币种/作用域错误、自奖励、审批独立、失败事务回滚、任务重启与账簿只入一次。真实渠道接入与真实到账不在本地沙箱验收内。
