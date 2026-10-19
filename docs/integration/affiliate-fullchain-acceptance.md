# 代理领取与独立模拟钱包全链路验收

日期：2026-10-19。代理领取已接入现有 `ex-wallet-sandbox`，使用独立测试租户和真实进程、HTTP、MySQL/SQLite 验证。没有直接写奖励或钱包表来模拟到账，也未替换当前演示服务。

## 同一笔佣金的验证结果

| 环节 | 结果 |
| --- | --- |
| 邀请注册 | 推荐人生成链接，新会员注册，来源自动投递并绑定直属关系 |
| 达标与计佣 | 充值10.00 USD、有效投注100.00 USD；第一层费率0.4000%，佣金0.40 USD |
| 日结审核 | 原worker自动调度；审核前不能领，申请人不能自审，独立审批后可领 |
| 受理领取 | HTTP 202仅表示受理，资金预留、持久化派发原单；重复领取不新增原单 |
| 独立钱包到账 | 钱包提交成功后故意丢失响应，代理状态保持处理中；独立钱包只有一条40分入账流水 |
| 超时恢复 | 恢复任务查询同一原单确认到账；实际派发POST共1次，未因超时重发入账 |
| 回执防重 | 实际钱包回执自动重试成功；同一签名回执重复提交两次均不重复记账；错误签名、站点、金额拒绝 |
| 冲正追偿 | 结果未知时原投注撤单保留待重试；到账确认后按原冻结方案重算为0，已付40分、追偿40分；不自动扣钱包 |
| 查询权限 | 会员只查本人派发记录，不能读取管理列表或由浏览器投递回执 |
| 只读对账 | 资金权益paid=40、target=0、debt=40；独立钱包余额40、流水1条；佣金内部钱包没有重复入账 |

测试证据位于本地 `.local/fullchain/external-final/`，配置与凭据不入Git。最终验证的领取编号为 `0d6d3e122d838aef84bd42a1d369bbc65601df9a83053062034fa499097cc2d5`，奖励编号为 `d90c3d2634bf1402823f41bd0f82c364f9d5357120bfcb3ac174026bcf45bd4d`；48个请求编号关联到业务审计，2条来源事实摘要一致。最终运行编号见同目录report.json和reconciliation.json。

## 实现与配置

API与执行器设置 `AFFILIATE_CLAIM_WALLET=wallet-sandbox`，并使用同一份服务端 `WALLET_SANDBOX_CONFIG_FILE`。默认未设置或值为internal时，仍走原内部钱包。配置须包含固定租户、应用、钱包站点、客户端及密钥；密钥不返回浏览器。

领取事务关闭原权益保留、预留资金并写入 `affiliate_wallet_deliveries`。执行器的 `affiliate.wallet.recovery` 在事务外派发；首次请求前持久化UNKNOWN。超时只查询原单；查询确认不存在时，适配器才可沿用同一商户原单重试。钱包站点和客户端在受理时冻结，配置变化不能把旧单转发到另一钱包。

到账由原单查询或验签回执确认，将预留转已付并标记奖励已领取。明确失败释放本次预留，保留其他风控限制，允许显式重新申请。并发租约和资金状态校验保证不同执行器不会同时提交同一领取。迁移表为 `036_affiliate_wallet_deliveries.sql`。

回执地址：`POST /v1/payment-callbacks/affiliate-wallet/{client_id}`。固定客户端映射站点后验签，再查原单归属；属于代理领取时只交代理模块处理，校验失败不回退。非代理原单再次按原结算钱包客户端绑定和签名校验后交原paymentService处理，未知原单拒绝。原结算回执地址继续可用；该路径不向浏览器开放。

只读接口：`GET /v1/me/affiliate/claim-deliveries`（本人）、`GET /v1/affiliate/claim-deliveries`（affiliate:read）。默认20条、最多100条，使用after_id稳定分页。前端将202显示为“领取已受理”，确认后才显示已领取和回执。

## 回归

- 五项真实MySQL资金状态测试及race检查通过：未知恢复和回执幂等、失败后重领保留其他限制、并发派发一次、钱包绑定变化拒绝、不支持币种不预留。
- 原独立钱包会员领取、后台派发及新增共用回执兼容测试通过，包含回执失败重试和原单对账。
- 原内部钱包完整链路重新通过，证据在 `.local/fullchain/internal-compat/`。
- 代理资格、任务租约、多方案、冲正、执行归属等12项顶层回归通过；后端全量编译测试、18项契约测试通过。
- 前端构建、32项测试、浏览器受理与到账状态展示测试通过。浏览器展示使用本次真实结果快照；资金交易验证来自真实进程接口链路。

【涉及其他业务】代理领取由同步内部入账扩展为可选异步独立钱包确认；原结算派发共用回执安全分流。原内部模式及原结算派发已回归。尚未接入真实资金服务，不代表生产验收完成。

## 重跑和边界

```sh
GOPRIVATE='github.com/ex-business/*' GOWORK=off go test -c -o .local/fullchain/runtime.test ./internal/runtime
python3 tools/affiliate_fullchain_acceptance.py --database-config /绝对路径/私有数据库配置.json --member-binary /绝对路径/模拟会员程序 --wallet-binary /绝对路径/模拟钱包程序 --web-dir /绝对路径/ExCommission-Web --output .local/fullchain/新目录
GOPRIVATE='github.com/ex-business/*' GOWORK=off AFFILIATE_FULLCHAIN_EVIDENCE_DIR="$PWD/.local/fullchain/新目录" go test -count=1 -v -run '^TestAffiliateFullchainEvidence$' ./internal/runtime
```

验收使用测试二进制专用业务时钟，未修改电脑时间或生产程序。来源水位由工具从原始已投递事实只读生成签名清单后提交；未验证来源系统自动跨日关闭。所有测试进程在结束时停止，保留隔离测试数据供核对。运行验收在独立分支完成；提交到main后仍需单独部署，当前演示入口未替换。

提交前复核：前后端独立审查；前端慢响应轮询改为上一轮完成后再调度。浏览器测试覆盖原有方案202受理至确认及超过3秒响应；独立方案组件已审查及构建，尚无专门浏览器用例。
