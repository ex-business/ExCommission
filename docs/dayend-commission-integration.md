# 三层业务量与日终等级接入

目录：[配置](#config) · [事实输入](#facts) · [日终冻结](#freeze) · [状态处理](#states) · [扩展与影响](#extension)

<a id="config"></a>
## 配置

启用 API 和 worker 的 `LOCAL_DAYEND_ENABLED=true`，默认关闭。配置通过现有方案草稿、模拟、发布审批生效，不绕过资格审批。原 `PREVIOUS_CALENDAR_MONTH` 策略继续使用原等级历史和月度统计。

日终方案将 `agent_policy.grade_window` 设为 `BUSINESS_DAY`，`max_depth=3`，追加：

```json
{
  "day_end": {
    "timezone": "Asia/Shanghai",
    "source_id": "sandbox",
    "qualification_policy_id": "demo-effective-v1",
    "member_depths": [1],
    "volume_depths": [1, 2, 3]
  }
}
```

| 字段 | 业务含义 |
| --- | --- |
| timezone | 明确的 IANA 时区，例如 UTC、Asia/Shanghai；Local 不允许。当地自然日采用左闭右开边界，支持夏令时。此字段控制定级统计日，不改变现有账单 UTC 日历。 |
| source_id | 计佣事实与有效会员事实的同一可信来源；来源扩展仍通过适配器接入。 |
| qualification_policy_id | 已存在、不可变的双门槛有效会员规则，币种须与方案相同。 |
| member_depths | 自定义统计哪些下级层级的有效会员人数，可选 1、2、3 的非空不重复组合。人数为业务日末之前已达标会员的累计去重人数。按日末关系归属统计。 |
| volume_depths | 自定义定级业务量统计哪些层级，与返佣层级分开；按订单业务时点关系归属统计。 |

定级条件支持 `EFFECTIVE_MEMBER_COUNT`（单位 COUNT）和 `TEAM_VALID_VOLUME`（单位方案币种），同一等级内所有条件同时满足，取满足条件的最高 rank。基础等级 rank=0 不设条件。日终策略拒绝月度净额指标，避免把未知事实当零。

计佣规则使用现有 `INDEPENDENT` 算法、`recipient=ANCESTORS`。`parameters.rates` 配齐产品、等级、depth=1/2/3 与 `rate_ppm`。每位受益代理使用自己的日终等级和原始层级。无自身返佣；无资格、停用或冻结的处理沿用旧计算器，不压缩层级。

日终版本必须从其当地零点生效，更新和回滚不能切断旧日终版本的自然日。切换不同日界线的时区需要单独安排迁移，不能直接中途改历史快照。

<a id="facts"></a>
## 事实输入

充值和达标业务事实继续按[有效会员接入](member-qualification-integration.md)提交。该累计仅用于有效会员认定，不直接产生佣金。

实际计佣继续提交现有 `POST /v1/events`，使用现有 Bearer、`Idempotency-Key` 和 `X-Source-Signature`（对原始请求 JSON 计算 HMAC-SHA256）。来源适配器把已确认、可计佣的投注、成交或消费统一映射为“有效业务量”；核心不新增专用投注流程。

```json
{
  "schema_version": "1",
  "source_id": "sandbox",
  "event_id": "settled-event-1001",
  "business_id": "order-1001",
  "event_type": "ORDER_CONFIRMED",
  "business_time": "2026-10-16T08:00:00Z",
  "external_user_id": "member-1001",
  "product_code": "VALID_VOLUME",
  "scene": "COMMISSION",
  "source_revision": "1",
  "payload": {
    "basis_amount": "100.00",
    "basis_currency": "USD",
    "confirmed": true
  }
}
```

产品、事件类型、scene 必须与已发布方案路由一致，以上仅为配置示例。`basis_amount` 是主币单位十进制定点字符串；有效会员接口中的 `amount_minor` 则是最小货币单位整数，两者不得直接混传。未达有效会员门槛的会员，其已确认业务量仍参与定级和计佣。

来源事件号及业务订单由现有接入模块去重。同一笔已结算订单分别驱动达标累计和计佣时，两个接口是不同用途，各自重试，不应把重复通知换成新的业务订单号。

<a id="freeze"></a>
## 日终冻结

来源完成该业务日投递后，由可信日终任务或管理端调用 `POST /v1/day-end-freezes`，需要 `plans:write` 和幂等键：

```json
{
  "plan_id": "volume-plan",
  "expected_plan_version_id": "published-version-id",
  "business_day": "2026-10-16",
  "source_watermark": "source-close-20261016-0001"
}
```

日终前拒绝冻结。`source_watermark` 是调用方提供的来源日结完整性凭证，必须等来源真正完成投递后填写；系统不会从任意字符串推断来源已完整。`cutoff` 是计佣事实按入库时间筛选的截止时点；有效会员人数读取冻结事务查询时已知的达标状态，并非该字段时点的历史状态快照。若来源继续并发投递回溯达标事实，两个指标的观测截止可能不一致，因此必须先完成并停止该日投递，再触发冻结。当前版本提供冻结接口，不自行猜测源系统何时完成日结，也不内置自动定时调用。

首次成功原子保存：发布版本、统计时区及层级、UTC 起止边界、已知事实截止时点、水位、任务 ID 集合、每个代理的统计值及日终等级，并放行等待任务。相同方案日期重复调用返回原快照，换版本或水位返回冲突，不能覆盖历史。失败时整笔回滚。

`GET /v1/day-end-freezes?plan_id=volume-plan&business_day=2026-10-16` 需要 `plans:read`，查询冻结结果。计佣任务快照额外返回 `day_end_evidence`，与原资格审批证据分别保存，便于解释计算等级与审批等级不同的情况。

<a id="states"></a>
## 状态处理

```text
Source confirmed business volume
              |
              v
      Existing signed /events
              |
              v
  SUSPENDED / DAYEND_PENDING
              |
      Source closes the day
              |
              v
 POST /v1/day-end-freezes
              |
              v
 Frozen statistics and grades
              |
              v
 QUEUED -> worker -> existing ledger
```

| 结果 | 后续处理 |
| --- | --- |
| DAYEND_PENDING | 正常等待日终，冻结后自动放行；不消耗依赖失败的重试次数。 |
| DAYEND_LATE_FACT | 订单不在已冻结任务集合内，保留原业务时间并挂起，不自动改写日终等级、不入账。当前没有迟到订单审批放行或历史等级自动重算接口；普通 resume 也不能绕过此检查。接真实来源时应在源端保证日结水位完整。 |
| UNKNOWN_FACT | 缺会员、关系、币种或等级依据，拒绝冻结或挂起；补齐来源事实后，以新幂等键重试失败的冻结请求。 |
| IDEMPOTENCY_CONFLICT | 同一方案日期已用其他版本或水位冻结，核对原结果，不能改号重算同一业务日。 |

现有已完成任务的退款和金额调整仍使用原流程；它们不重写日终等级。有效会员退款撤销语义尚未提供，不能据此声称已具备退款后自动取消有效会员资格。

<a id="extension"></a>
## 扩展与影响

新增 `dayend/domain、ports、application、adapters`；代理主体、有效会员和计佣事实分别通过所属模块提供只读端口。日终结果保存在独立 `dayend_snapshots` 表，不写旧月度等级历史。日终与等待任务共用日锁，避免并发冻结时漏唤醒。

当前本地读取适配器最多扫描一个来源一天的 10000 条计佣事实，超出明确拒绝冻结，绝不截断后按零或部分数据出账。大批量接入时替换 `Tasks.DayFacts` 与统计读取适配器为预聚合实现，保留快照及纯计算器契约。

【涉及其他业务】影响链路为方案校验与发布/回滚、有效会员只读统计、历史代理关系、worker 快照与任务状态、现有计佣入账。旧月度策略不携带 day_end 字段，仍走原路径；旧等级评估拒绝日终策略，避免串写。公共 base、鉴权、站点识别和公共响应未修改。

验证包括：自定义直属/三层统计、三个受益人不同等级、无资格中间层不压缩、未达标会员业务量、并发冻结与重复入账保护、迟到挂起、HTTP 认证与查询、夏令时23/25小时日界线、原有全量 MySQL 回归。真实订单来源、自动日结调度和管理页面配置控件未接入本次实现。
