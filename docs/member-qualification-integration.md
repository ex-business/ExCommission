# 有效会员：充值与有效投注双门槛

目录：
- [目标与模块边界](#scope)
- [前置条件与配置](#preconditions)
- [字段判断](#fields)
- [可定义的人数统计范围](#member-statistics)
- [调用顺序](#api)
- [状态与达标时间](#status)
- [接入流程](#flow)
- [幂等、异常与扩展](#extension)
- [验证及其他业务影响](#verification)

<a id="scope"></a>
## 目标与模块边界

受邀注册会员的成功充值累计与参与场馆的已结算有效投注累计均达到规则门槛时，记录有效会员及真实业务达标时间。不限达标时长，以同一币种的整数最小货币单位计算，不混币、不使用浮点。

本次使用独立 `qualification/domain/application/ports/adapters` 模块。领域只计算双门槛；应用层编排事务；来源端口验证可信事实；注册读取适配器通过注册模块的只读 Repository 取得首次邀请归属。所有新增持久化表属于 qualification 模块。

本模块没有修改现有计佣事件格式、算法、等级评估、账簿、注册审批、公共鉴权或响应实现。组合根只增加模块装配。有效会员与代理方案的参与资格、团队等级是不同事实，不能因为会员达标就绕过原审批授予等级或发奖。

<a id="preconditions"></a>
## 前置条件与配置

1. 会员已通过可信注册事件完成代理身份与首次关系接入。未注册或只有旧同步记录时不能猜测邀请归属。
2. 使用已认证主体所在的租户、应用，不接受请求体指定站点或跨站点查询。
3. 新模块默认关闭；明确设置 `LOCAL_QUALIFICATION_ENABLED=true` 和独立 `LOCAL_QUALIFICATION_KEY` 后重启 API。初始化工具会生成缺失的独立密钥，Compose 已透传配置。
4. 当前只装配本地 `sandbox` HMAC 来源。真正接入充值及注单服务时实现 `qualification/ports.Source` 并显式装配，使用独立的真实服务主体及密钥。接口示例中的成功类型不是可信凭证，不能由浏览器自行提交成功状态。
5. 当前货币沿用已有目录 USD、BTC、POINT。门槛由不可变规则指定；未支持的货币必须另行接入目录，不能假装换汇。

<a id="fields"></a>
## 字段判断

| 字段 | 产品与调用方的决策 |
| --- | --- |
| policy_id | 不可变规则编号；改变门槛、场馆、统计范围或时区必须建立新编号，不覆盖历史规则 |
| member_statistics | 可选人数统计策略，range 与 timezone 创建后锁定；旧请求未携带该字段仍保持原永久统计口径 |
| source_id | 服务端明确装配的来源；事件、会员和规则必须属于同一来源 |
| currency | 门槛及两种累计统一使用该币种；其他币种明确拒绝 |
| amount_minor | 整数最小货币单位；USD 的 5000 表示50美元，不是5000美元 |
| event_type | 只接受 DEPOSIT_SUCCEEDED 和 BET_SETTLED；待支付、失败或未结算订单不能累计 |
| venue_type | 充值传 null；投注必须属于规则 venue_types，金额表示该已结算订单的有效投注额 |
| parent_agent_id | 首次邀请归属；null 的根代理不计为受邀有效会员，即使本人达到双门槛 |
| is_effective | 是否为达标的受邀会员；只有 true 才能被后续有效人数统计消费 |
| qualified_at | 在业务时间序列中首次同时满足门槛的时间；未达标为null |
| first_observed_at | 服务首次发现达标的处理时间，保留首次值；不能代替业务达标时间 |
| revision | 新订单事实导致的状态修订；事件重投和订单别名不增加修订 |

<a id="api"></a>
## 调用顺序

所有接口沿用现有 Bearer 身份和响应信封 `{data,request_id}`；写请求必须提供 `Idempotency-Key`。完整结构见 `contracts/v1/openapi.json`。

### 1. 建立不可变达标规则

`POST /v1/member-qualification-policies`，当前本地权限 `agents:write`：

```json
{
  "policy_id": "demo-effective-v1",
  "source_id": "sandbox",
  "currency": "USD",
  "deposit_threshold_minor": "5000",
  "valid_bet_threshold_minor": "10000",
  "venue_types": ["SPORTS"],
  "member_statistics": {"range": "NATURAL_DAY", "timezone": "UTC"}
}
```

两项门槛都必须大于0，场馆至少1项且不重复。同编号同配置可重投；改变内容返回409。初期显式指定规则编号，后续站点配置可持有版本映射，不能静默把旧事实改套新门槛。

### 2. 来源服务提交最终成功事实

`POST /v1/member-qualification-facts`，当前本地权限 `users:sync`，另需 `X-Qualification-Signature`：

```json
{
  "schema_version": "1",
  "policy_id": "demo-effective-v1",
  "source_id": "sandbox",
  "event_id": "deposit-event-001",
  "business_id": "deposit-order-001",
  "event_type": "DEPOSIT_SUCCEEDED",
  "external_user_id": "注册服务的外部会员编号",
  "business_time": "2026-10-16T12:00:00Z",
  "amount_minor": "5000",
  "currency": "USD",
  "venue_type": null
}
```

投注事实将类型改为 `BET_SETTLED`，填写该结算订单的有效投注额及参与场馆代码。业务时间必须不早于会员注册、不晚于服务端当前时间，最多微秒精度。零有效投注可记录，但不增加累计；零充值拒绝。

签名为独立密钥对以下 LF 前缀加原始请求字节进行 HMAC-SHA256 后的十六进制结果：

```text
qualification-v1\n{tenant_id}\n{app_id}\n{source_id}\n
```

不能复用注册或计佣签名，不能签名后重新序列化请求。注册完成后再投递依赖该会员的事实。

### 3. 查询达标状态

`GET /v1/member-qualifications?policy_id=demo-effective-v1&source_id=sandbox&external_user_id=会员编号`，权限 `users:read`。

规则和已注册会员存在但没有事实时返回200、零累计及未达标，不是未知会员。已达标示例核心字段：

```json
{
  "deposit_total_minor": "5000",
  "valid_bet_total_minor": "10000",
  "is_effective": true,
  "qualified_at": "2026-10-16T12:05:00Z",
  "first_observed_at": "2026-10-16T12:05:01Z",
  "revision": 3
}
```

<a id="status"></a>
## 状态与达标时间

只满足一项门槛时仍为未达标。两项均满足且存在首次邀请归属，才设置 `is_effective=true`。用户是否为代理、代理是否拥有等级/方案资格不在本模块重复决定。

事实按 `business_time` 重算该会员的业务时间线；顺序相反的投递得到一致结果。迟到的正向事实可能将真实 `qualified_at` 校正到更早时间，`first_observed_at` 始终保留首次观测时间。每次新事实生成修订和同事务 `member.qualification.updated.v1` outbox，后续消费者按事件号及revision处理，不能简单忽略“仍为true但业务时点已校正”的修订。

当前只实现不可变正向成功事实，保留首次达标历史；退款、撤单、冲正导致的资格撤销规则待业务确认后单独扩展。不能通过负金额、改写旧订单或使用新事件号覆盖旧事实代替冲正。

<a id="flow"></a>
## 接入流程

```text
+----------------------+       +-------------------------+
| Registration source  | ----> | Registered member/agent |
+----------------------+       +-------------------------+
                                             |
+----------------------+                     v
| Deposit final event  | --+     +-------------------------+
+----------------------+   +---> | Qualification source    |
+----------------------+   |     | signature + identity    |
| Settled valid wager  | --+     +-------------------------+
+----------------------+                     |
                                             v
                                +-------------------------+
                                | Member-scoped transaction|
                                | event/business dedup     |
                                | totals + times + revision|
                                | transactional outbox     |
                                +-------------------------+
                                             |
                                             v
                                +-------------------------+
                                | Reader / future consumer|
                                | grade, referral rewards |
                                +-------------------------+
```

<a id="extension"></a>
## 幂等、异常与扩展

- 来源事件号防跨订单复用；规则/来源/事件类型/业务订单号防同订单换事件号重复累计。订单属于不同会员或金额等事实变化时409，不能重写。
- 同会员同规则行锁串行累计；状态和事实采用当前读，避免等待锁前建立的快照遗漏并发已提交事件。其他会员不共用可写累计状态。
- 新事实、事件别名、金额累计、修订和outbox同事务；任一步失败全部回滚。重复事实既不增加修订，也不重复发布事件。
- 400表示字段、金额、场馆、币种或时间不合规则；403表示身份/来源签名不可信；404表示规则、会员或注册证据尚不存在；409表示事实或规则冲突。瞬时故障保留原事件重试。依赖未就绪的404先补齐规则/注册，再按原事件重投，不改会员或邀请关系。
- `qualification/ports.Reader` 为后续模块提供独立读取契约；当前适配器只读取注册模块，不跨模块写表。还没有自动接入旧的上一自然月等级统计，不会改变原佣金计算结果。
- 初期按单会员读取有索引事实并重算，优先保证乱序一致性；大规模生产可在相同端口后增加快照/分页累计，容量未验收。
- 新迁移017只创建本模块四张表，不改变历史表或迁移；关闭模块开关停止新入口，保留历史数据及outbox，不用删账簿回滚。

<a id="verification"></a>
## 验证及其他业务影响

真实本地验证：充值门槛50 USD、有效投注100 USD。充值30后false，投注100后仍false，再充值20后true且有达标时间；重复充值订单不再次累计。注册事实由临时会员项目提供，模拟成功订单来自其独立CLI适配器，没有真实资金流动。

Go纯领域及来源测试、真实MySQL并发累计/双键防重/原子回滚/迟到事实测试通过；原用户、代理、注册与完整佣金链路race回归通过。接口结构检查通过。

【涉及其他业务】调用链为会员注册证据 -> 可信充值/注单事实 -> 有效会员状态 -> 未来等级/邀请奖励消费。已验证本地沙箱注册及合成订单接入；真实充值、游戏结算、退款撤单、站点配置、团队等级消费和发奖未联调。本次没有修改它们的现有模块，不声称业务全链路上线完成。

<a id="member-statistics"></a>
## 可定义的人数统计范围

口径依据：[代理配置原型](https://51z4k4.axshare.com/_4_1%E3%80%90admin%E3%80%91%E4%BB%A3%E7%90%86%E9%85%8D%E7%BD%AE.html)。

此配置归属代理业务下的有效会员规则模块；等级模块引用规则结果，返佣模块继续按等级和层级比例计算。原型中的“范围”是按首次真实业务达标时间 `qualified_at` 筛选人数，不是清零充值或有效业务量累计。会员仍可以跨日、跨周完成双门槛。

在创建规则请求中配置 `member_statistics`，新规则建议显式选择原型默认的 `NATURAL_DAY`：

| range | 含义 |
| --- | --- |
| NATURAL_DAY | 当前自然日内首次达标的有效会员，下一天重新统计人数 |
| NATURAL_WEEK | 周一零点至下一周一零点，下一周重新统计人数 |
| NATURAL_MONTH | 月初零点至下月初零点，下月重新统计人数 |
| FOREVER | 所有已达标有效会员，永久累计 |

`timezone` 使用明确 IANA 时区，例如 `UTC`、`Asia/Shanghai`。不接受 `Local` 或未知时区。日终策略引用有限自然周期规则时，两者的时区字符串必须一致，避免业务日与会员统计日错位；规则与日终所用币种、来源也必须一致。

同一 `policy_id` 的统计范围、时区、门槛、场馆全部不可覆盖：同配置重投返回原结果，改变任一配置返回409。需要新口径时创建新的规则编号，再通过原方案审批流程引用；新规则下需按接入流程重放其所需事实，不自动挪用旧规则的事实。已冻结的历史日终结果不重算。当前没有修改或删除规则的接口。

兼容旧规则：持久化 JSON 或旧客户端未携带 `member_statistics` 时，仍按 `FOREVER` 统计，避免升级后原有会员人数骤降。不得把旧规则缺省解释为新规则推荐的按日口径。

### 读取配置

`GET /v1/member-qualification-policies/{id}`，需要 `agents:read`，返回原始不可变配置；不存在返回404。旧规则可能没有 `member_statistics` 字段，按上述兼容规则处理。

### 预览人数

`GET /v1/member-qualification-statistics?policy_id=demo-effective-v1&source_id=sandbox&as_of=2026-10-17T00%3A00%3A00Z`，需要 `users:read`。`as_of` 为排他截止点，不接受未来时间或超过微秒精度的值。

```json
{
  "data": {
    "window": {
      "range": "NATURAL_DAY",
      "timezone": "UTC",
      "start": "2026-10-16T00:00:00Z",
      "end": "2026-10-17T00:00:00Z",
      "as_of": "2026-10-17T00:00:00Z"
    },
    "agent_ids": ["qualified-agent-id"],
    "count": 1
  },
  "request_id": "request-id"
}
```

自然周期按截止点之前一微秒定位：零点的日终请求统计刚结束的一天、周或月。只有 `qualified_at >= start`、`qualified_at < end` 且 `qualified_at < as_of` 的会员被选中。同一代理只计算一次，未达标会员和非邀请根代理不计入人数。永久范围的 `start/end` 均为null。

周/月中途预览时，`end` 表示整个自然周期末尾，可能晚于 `as_of`，人数仍只统计截止点之前的达标会员。结果是该规则下的有效会员集合；等级模块另按代理关系和配置的下级层级归属统计。

该预览读取当前已知达标状态，`as_of` 筛选业务达标时间，不代表当时已观测到的历史数据库快照；迟到事实可能校正 `qualified_at`。日终仍要求来源完成并停止该日投递后冻结，已冻结快照不随预览变化。

```text
Immutable qualification rule
          |
          v
Successful facts -> lifetime threshold accumulation
          |
          v
First business qualification time
          |
          v
Configured day / week / month / forever selection
          |
          v
Day-end grade statistics -> existing commission calculation
```

【涉及其他业务】日终模块改为消费有效会员模块的只读统计端口，快照新增可选 `member_statistics`，保存实际使用的范围、时区、自然周期及截止点。旧快照仍可读取；计佣费率算法、账簿和资格审批不变，公共 base 未修改。时区库随二进制嵌入，已在无系统时区文件的 scratch 容器验证 Asia/Shanghai 可用。本次只新增后端配置、读取与预览能力，管理页面控件尚未接入。
