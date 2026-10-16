# ExCommission

Go 模块化佣金分润系统，业务标准 V1。按顺序实施，未完成的能力不得宣称可用。

## 本地环境

使用 Docker Compose 和 MySQL 8.4。复制环境配置后填写本地专用密码：

```sh
python3 tools/init_local.py
docker compose up -d mysql
docker compose --profile verify run --build --rm contract-check
```

数据库仅绑定 `127.0.0.1:13316`，数据保存在项目专用卷。`.env` 不进入 Git。

## 契约与验收

- `contracts/v1/openapi.json`：接口结构契约。
- `contracts/v1/*.schema.json`：严格配置、事件与快照结构。
- `conformance/v1/manifest.json`：标准原件摘要、条款 ID、实施任务与计划测试 ID。计划测试 ID 不表示测试已经实现。
- `contracts/v1/compatibility.txt`：旧原型迁移边界及不支持能力。
- `tools/test_contracts.py`：契约检查工具；业务后台统一使用 Go。

原始参考资料和本地开发指令保存在 Git 排除目录，不进入镜像与提交。结构契约检查不替代 Go 业务、并发、账务和故障验收。

## Go 服务

锁定 Go 1.26.5，模块化单体，API 和 worker 可独立启动：

```sh
docker compose up -d --build api worker
curl http://127.0.0.1:18080/healthz
```

/healthz 检查进程，/readyz 检查数据库；worker 已装配持久化任务、付款恢复、退款恢复与 outbox 投递。

模块下的 `domain/application/ports/adapters` 分别负责领域、用例、边界和适配器；不得跨模块直接写表。计算领域只依赖标准库，金额采用任意精度整数、八位定点，禁止浮点。

## 数据库与事务基础

API/worker 启动时通过带摘要校验、可重入的版本迁移初始化 MySQL，并通过 `/readyz` 检查数据库可用性。业务模块共用应用层事务，不提前提交；命令结果、业务写入、审计与 outbox 可在同一事务内完成。

```sh
docker compose --profile verify run --build --rm database-check
curl http://127.0.0.1:18080/readyz
```

测试使用独立随机租户并清理自己产生的数据。构建仅执行格式、静态检查和编译；业务测试按当前改动影响范围显式运行，避免每次构建重复测试未改功能。当前身份提供方只实现本地沙箱接口，未接生产身份源；业务 API 的授权装配随各功能落地。


## 用户与代理沙箱

先运行 `python3 tools/init_local.py`，再运行 `docker compose up -d --build api worker`。本地模式使用 `.env` 中两个独立身份令牌，分别用于申请和审批；不存在公开管理员注册。更改授权后重启不会自动恢复已撤销权限。

可在 `.local/users.json` 提供明确的本地测试用户数组，每项包含 `external_user_id` 和 `display_name`。该来源固定为 `source_id=sandbox`、`mapping_version_id=1`，不代表任何真实外部来源已联通；空数组不会制造用户。

已实现用户同步与结果查询、代理创建、关系迁移申请、独立审批、历史祖先分页查询。关系不压缩物理层数，游标绑定租户、应用、代理、时间与分页条件；并发互绑通过作用域锁防止成环。合同端口按半开有效区间读取且拒绝重叠，不允许合同覆盖佣金费率。等级和方案资格通过下述历史版本机制实现。

## 方案与等级

支持方案草稿修订、严格配置校验、试算证明、独立发布审批、业务时点选版、版本分页和回退为新版本。审批中不可编辑；驳回增加修订号并使旧试算失效；发布执行失败不会伪装生效，也不会锁死草稿。历史配置不覆盖。

模拟与后续正式计算使用同一纯计算入口；已注册 PERCENT、FIXED、UNIT_PRICE、INDEPENDENT、DIFFERENTIAL、MARGINAL、HIGHEST_RATE、WEIGHTED v1；未知算法或版本明确挂起。

等级按方案独立保存，人工资格变更走独立审批。每日 UTC 等级评估使用上一自然月及明确截止水位的统计快照，不会自动授予资格。`.local/grade-statistics.json` 是可选的受控沙箱统计输入，默认空对象会返回 UNKNOWN_FACT；并不表示接通真实业务来源。统计输入包含 plan_id、calendar_period、cutoff、source_watermark、agents（按代理ID索引的三个字符串指标），验收记录保留在数据库，不因源文件改变而覆盖。


## 固定数值验收

22 项固定向量原件仅在本地参考目录，通过只读挂载交给 Go 验收；独立 Python 参考器读取同一原件作交叉验证。仓库不包含原始参考包。Go 的条件、资格、上限、预算和退款数值均使用任意精度整数，不依赖系统当前时间。

WEIGHTED 保留原始候选权重后分配，不合格份额归平台。显式非祖先受益人以 depth=0 表示，独立冻结资格与状态，祖先物理链不压缩。单费率不超过100%；资金上限是合计预算，可明示超过100%，仍受方案总预算约束。

## 可信事件与任务

本地来源 ID 为 `sandbox`，以 `.env` 中的 `LOCAL_SOURCE_KEY` 对原始请求字节作 HMAC-SHA256，十六进制签名置于 `X-Source-Signature`。仅传 confirmed=true 不构成可信确认。原始请求和业务规范化摘要分开保存，凭据不进入响应或日志。

事件按来源事件ID和业务对象双键防重；新事件ID重复业务保留别名，别名不能用于另一业务。POST 返回的 event_id 是接收资源ID，source_event_id 保留来源事件ID。

worker 使用持久化租约、fencing token和冻结快照。快照仅按业务时点查询方案、关系、等级和合同，不用当前值补历史。仅基础设施瞬时失败自动重试5次，间隔1/5/30/120/600秒；业务缺口挂起，耗尽后DEAD。`POST /v1/calculation-tasks/{id}/resume` 要求 expected_revision、evidence_ref 和 reason，恢复原任务而不是创建新业务。

正式任务只有在权益、账簿与任务状态原子提交后才标为成功。普通用户若没有明确的历史直属代理绑定，会挂起 RELATION_HISTORY_MISSING；业务用户本身是代理时从其直属上级开始，禁止给自己奖励。

正式任务现已接入权益账簿：每个正金额结果生成不可变的原始目标、pending 余额及一张等额借贷分录；事件业务键、方案版本、规则、受益人及币种共同确定权益身份。零金额仅保留计算解释。权益、分录、冻结原因、outbox 与任务成功状态共用事务，租约隔离校验失败全部回滚。冻结仅限制原余额，不增加金额桶；后续结算和付款通过账簿端口转移余额并追加分录。

结算通过 `POST /v1/statement-runs` 按已结束的 UTC 日期生成草稿，通过 `GET /v1/statements/{id}` 核对权益与摘要，再提交独立审批。一个作用域内结算期按日期向前推进，已生成周期与筛选集合固定；重复运行返回原单，改变筛选集合返回冲突。迟到事实保留原 business_time，在后续运行中收录。提交才占用权益；拒绝或执行失效释放占用。批准时重新检查每个权益的修订、金额及冻结状态，整体转入 available；过时审批不会部分执行。

支付使用独立本地沙箱：创建时锁定结算修订并占用 available，渠道调用在事务外执行。worker 恢复未发送付款，对 SUBMITTED/UNKNOWN 仅按原 merchant_payment_id 查询。`POST /v1/payment-callbacks/sandbox` 必须通过 `X-Payment-Signature` 验签；重复通知幂等，金额/币种不符或终态冲突进入对账异常，保留原资金结果并冻结关联权益。沙箱默认只返回 UNKNOWN，终态必须来自显式签名回调或 `.local/payment-results.json` 中的查询/对账记录；这不代表真实银行到账。付款状态通过 GET 查询，创建响应保留最初的受理结果。

累计退款由签名来源提交，先于原业务的记录等待原快照；首次正式入账会合并已确认累计值。每次使用原资格、候选人、算法与上限求新目标，固定/加权奖池部分退款不变，全额归零；单位单价必须有累计撤销数量。低累计值是 STALE_NOOP，超过已知原额拒绝；原业务尚未知时收到的超额记录，在原事实确定后标为 REJECTED。reserved 未决时挂起资金调整并冻结，其结果明确后继续冲减；已付不足部分增加 debt，历史 paid 不覆盖。

退款或事实更正改变已批准权益时，剩余 available 回 pending，旧付款授权失效，后续结算必须重新审批。独立审批的 FUTURE_OFFSET 只使用同代理同币种已批准可用佣金并按债务 FIFO 执行；抵债作为非现金结清记入该未来权益 paid，债务权益同时累计 recovered，现金支出仍由账簿 CASH_CLEARING 口径识别。CASH_CONFIRMED 必须有服务端核验的确认凭证，一份凭证不能重复追回；本地凭证文件只是沙箱事实，不代替银行核验。

`POST /v1/adjustment-requests` 的 corrected_facts 必须提供完整事实，保留原 business_time、币种和业务路由；沿用原版本及资格快照。通过独立审批后追加新修订和差额，不覆盖原任务快照与已付记录。通过任务 `/adjustments` 查询有效修订，再提交 expected_revision；后续退款沿用最新已批准事实修订。

风险冻结支持多个独立原因、申诉和独立审批解除。限制金额作为 pending/available 的受限子集展示，reserved 上的限制显示 payment_hold，不能据此强行释放未知付款。解除会把剩余 available 回 pending，必须重新结算；退款等待原因由退款流程自动处理，不能手工绕过。代理状态变更同样独立审批并保存 [from,to) 历史，当前 FROZEN 拒绝新付款，关闭账户前必须清理未付余额和债务。

每个授权请求均记录安全或操作审计，命令与实际后台事务的审计和业务同事务提交，审计失败不吞掉。拒绝访问、读取与下载另记访问结果，只保留主体、请求号、动作、资源引用和白名单结果；不记录令牌、签名、完整载荷或凭证。安全事件处理追加记录并单独维护处理状态，不改写原审计。`GET /v1/audit-logs` 支持固定 as_of、筛选及签名分页游标；翻页必须携带返回的 as_of 和原筛选条件。

权限通过 `POST /v1/permission-change-requests` 提交显式增删清单，独立审批后生效并保留主体修订；只允许受控初始化已登记的主体，不提供公开注册管理员入口。每次请求实时读取授权，不通过重启恢复撤销权限。审计投递使用原 outbox 事件号防重，只记录事件引用与摘要；失败保留待投递事件，不丢弃原记录。

报表从同一 MySQL 一致性读视图读取账簿并返回 as_of、水位及货币目录版本；收益、净佣金成本按币种分别展示，不推算缺失收入的利润或缺少归因来源的转化率。分录保留权益追溯关系；要求方案/代理筛选而旧记录缺少可核验关联时明确拒绝，不悄悄漏数。当前单次最多 10000 条明细，超过后要求缩小筛选，不截断冒充完整结果。

CSV 导出在数据库保存固定快照与摘要，生成和下载分别鉴权、审计，默认24小时过期。旧快照不因后来退款改变；过期返回410，同幂等键仍指向原导出，不重新生成。CSV 文本会中和公式前缀。最低付款和单笔上限来自权益原方案，在结算摘要冻结；不足最低额留待下周期重新汇集和审批。

公共设置仅支持已编译并验收的能力：USD(2)、BTC(8)、POINT(0)，UTC每日历，签名来源沙箱、付款沙箱及本地通知收件箱。货币/日历新版本须独立审批并从未来 UTC 周期边界生效，历史版本不可改写；精度与未实现渠道不允许通过配置虚构。连接开关有修订控制，关闭来源拒绝新计佣事实，关闭付款渠道仍可核验旧资金号。组件接口只返回 credential_ref，不返回明文凭据；通知只写本地防重收件箱，不对外发送消息。

故障恢复：`tools/verify_local_restore.py` 仅对本 Compose 项目执行本地演练，使用独立合成租户和临时恢复库。演练核对全表行数、迁移记录及资金核心表摘要，再在恢复库核验原资金号、重放签名回调和业务事件、检查守恒；源库未知付款不被改动。临时备份、恢复库、临时授权及演练数据在结束后清理，项目服务恢复。

APPROVED/FAILED 审批可通过 `/v1/approvals/{id}/execution-retries` 附证据恢复原执行；重新鉴权并校验原摘要、修订及领域状态，不改原批准人和批准时间，不重新审批绕过版本。待审申请可由申请人撤回，领域占用随之释放。历史等级快照保留历史修订、来源、证据与有效区间；代理状态历史缺失时挂起，不能误作普通用户。

生产准入见 `conformance/v1/production-readiness.json`。当前真实身份、业务来源、支付渠道及机器/负载/SLO/RPO/RTO未提供，仅本地沙箱通过；可靠性并发场景和本机恢复观察不替代生产容量与恢复目标验收。

## V1 本地交付验收

15 项顺序实施任务已完成本地验收，S01–S20 对照见 `conformance/v1/acceptance-matrix.json`。完整 HTTP 生命周期覆盖59个公开操作、176次请求；77个请求体及176个响应均按 OpenAPI 校验。22个固定向量通过 Go 与独立 Python 双重核验。故障、并发、竞态及59表隔离恢复演练通过；生产准入仍受上述缺失条件阻塞。

用户同步后可用 `/v1/users/lookup` 获取内部用户ID。公开计算快照使用契约规定的 snake_case，保留历史资格、关系、合同证据；付款响应提供 amount 与 receipt。结算项冻结目标与历史 paid 基线，目标变化使旧付款审批失效；缺失该快照的旧单返回 UNKNOWN_FACT，禁止臆造回填。混合方案付款上下限取交集，交集为空时拒绝付款。付款终态冲突只能由核验对账消除异常状态，风险冻结仍须独立解除。

审批及执行恢复在幂等结果返回前重新核验当前领域权限，撤权后不能借旧请求重放绕过。签名使用本地环境变量字符串的 UTF-8 字节作为密钥，不做十六进制解码。真实身份、来源与银行渠道尚未接入，沙箱回调不代表真实到账。

日常停止使用 `docker compose stop`，启动使用 `docker compose up -d`；升级前应保存项目数据库与受控配置。恢复演练命令为 `python3 tools/verify_local_restore.py`，仅限本地项目；演练临时备份会自动删除，不替代持久备份方案。回退应恢复匹配版本的数据库和配置，禁止删除账簿或覆盖历史记录来撤销资金结果。原型、业务原件、AGENTS.md 与 .env 仅留本地，不进入 Git。

## 简单登录与第三方身份

账号密码登录与第三方令牌核验已接入，契约见 `docs/integration/authentication.md`。运行 `python3 tools/init_login.py` 生成本地 operator / approver 两个账号，密码仅保存在 `.local/login-credentials.json`（0600）。四个接口为 `/v1/auth/config`、`/v1/auth/login`、`/v1/auth/me`、`/v1/auth/logout`。本地会话8小时到期，重启失效；现有固定令牌只兼容服务调用。前端通过服务端代理保存令牌。第三方配置文件 `.local/external-identity.json` 默认空对象关闭，配置HTTPS令牌核验与显式账号绑定后启用；真实平台尚未接入。

## 通用累计达标奖励

新增 milestones 模块，配置活动、单一权威来源、1至16个累计指标、ALL 组合条件与 ONCE_PER_SUBJECT 次数限制。受邀会员充值和有效投注同时达标只是配置示例；件数等其他累计指标使用同一流程。详细契约见 `docs/integration/milestones.md`，示例见 `conformance/v1/plan-milestone.json`、`milestone-program.json`、`milestone-observation.json`。示例未来日期与标识必须替换为业务确认值，不是已激活活动。

先发布 MILESTONE_REACHED 类型的 DIRECT/FIXED 方案，独立审批活动配置，再由可信来源签名提交会员累计快照。首次事实冻结活动内奖励代理及证据，不覆盖全局关系；累计值只增不减，来源必须按活动窗口统计确认量。全部指标达标时，活动会员进度、唯一触发记录和原生计佣事件/任务原子提交，100并发仍只有一个任务。外部普通事件接口拒绝伪造该内部事件。达标证据、活动摘要及审批引用写入冻结快照；付款仍需结算审批与渠道确认，任务触发不等于到账。

新增接口：POST `/v1/milestone-program-requests`、POST `/v1/milestone-observations`、GET `/v1/milestone-programs/{id}`、GET `/v1/milestone-programs/{id}/members/{user_id}`。充值和有效投注的真实性由接入来源提供，本系统不自行推算不存在的数据；可回退净值及自动撤回已发权益不在本次单调累计合同中，纠错沿用独立审批更正/追回。

本次使用独立HTTP服务与现有Docker MySQL验收，功能回归、竞态与接口结构检查通过；Docker管理/构建接口当前返回500，新容器构建及部署尚未完成，18080现有进程仍是此前版本。具体边界见 `conformance/v1/milestone-acceptance.json`。

## 每日自动结算与审核

worker已装配 `settlement.daily`：按UTC每日01:00生成前一日全量结算，并为首次提交的草稿自动生成STATEMENT待审核记录。只生成待审核，不自动批准或付款。结算、权益占用、审核、审计与持久游标同事务；并发与重启不重复生成。首次启用从昨日开始，之后离线跨日按游标逐日补齐，空周期也记录完成。首次失败保留起点，不因次日重启跳日。

`GET /v1/settlement-schedule` 使用 `statements:read` 权限查询UTC调度时间、下一周期与最近生成记录。任务每5秒检查，按已注册的业务期日历选版。scope来自worker的LOCAL_TENANT/LOCAL_APP，与API一致；默认local-sandbox/commission，不扫描其他租户。

详细合同见 `docs/integration/scheduled-settlement.md`。同周期手工全量运行可复用，已有审核不重提；不同筛选、已过时草稿或跳过的历史周期冲突明确失败并保留游标，禁止静默改写历史。冻结、最低额、迟到事实和后续周期收录仍遵循原结算规则。

本次定时边界、100并发、审计异常回滚、漏跑补偿、手工审核记录复用、当前权限及状态接口结构已在隔离租户与现有Docker MySQL验证。Docker引擎管理接口仍返回500，本次镜像部署未成功；当前运行服务不代表新调度已经启用。验收边界见 `conformance/v1/scheduled-settlement-acceptance.json`。

## 佣金审核与批量派发

审核列表 `GET /v1/commission-reviews` 支持状态、代理、币种、日期及分页；`POST /v1/commission-review-batches` 每项独立复核修订、摘要、当前权限及禁止自审，保存审核备注，单项失败不回滚其他项。同键更换清单返回冲突。

新增 payout-orders 派发草稿，已批准结算可准备本次金额、修改备注或取消。草稿不占款，不改原佣金权益；只有显式RELEASE后进入队列。后台 `payout.instructions` 复用原付款创建防重键，关联失败或worker重启只恢复原付款，批量逐项处理。QUEUED之后禁止编辑及本地取消，付款UNKNOWN继续原商户号查询。原付款操作接口保持兼容，不声称取消本地草稿等于银行撤单。

详细契约见 `docs/integration/commission-review.md`。本次有独立前端专用审核页面，支持筛选、单笔/批量审核、逐项结果、金额与备注、草稿取消及批量提交；浏览器验收使用模拟HTTP，后端资金闭环独立使用现有Docker MySQL验证。Docker管理接口仍异常，新容器联部署尚未完成。
