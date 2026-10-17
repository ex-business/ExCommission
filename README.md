# ExCommission

代理与佣金业务后端，负责规则配置、佣金计算、独立审批、结算和账务记录。

## 系统关系

![ExCommission系统关系](docs/images/architecture.svg)

| 工程 | 职责 |
|---|---|
| **ExCommission** | 执行业务规则、审批、计算、结算和报表 |
| [ExCommission-Web](https://github.com/ex-business/ExCommission-Web) | 代理管理后台和会员代理页面，入口19486 |
| [ex-ops-identity](https://github.com/ex-business/ex-ops-identity) | 运营账号、站点和角色；配套管理页面入口19586 |
| [ex-member-sandbox](https://github.com/ex-business/ex-member-sandbox) | 本地模拟会员注册、登录及邀请接入 |

运营账号和会员账号独立。管理页面由ExCommission-Web提供，ExCommission本身是API与后台任务服务。

## 主要功能

| 功能 | 包含内容 |
|---|---|
| 条件与方案 | 参与范围、等级条件、收益规则、试算、发布、历史版本 |
| 代理与邀请 | 代理关系、资格等级、三层返佣、推荐及受邀奖励 |
| 运营配置 | 站点开关、充值/投注门槛、奖励阶梯、去重规则、社媒分享 |
| 结算与账务 | 佣金计算、日结、独立审核、领取、付款对账、退款修正 |
| 报表与风控 | 收益/财务/成本报表、导出、冻结解除和安全审计 |

## 业务流程

```text
Configure -> Simulate -> Submit -> Independent review -> Activate
Facts -> Calculate -> Settle -> Review -> Claim / Payment receipt -> Report
```

申请人不能审批自己的申请。每个业务请求校验所属站点；资金操作保留事务、幂等和原单核验。

## 继续阅读

- [工程说明与真实页面截图](docs/工程说明.md)：职责、登录权限、配置和运行方式。
- [代理规则及验收说明](docs/integration/affiliate-risk-calculator-locales-acceptance.md)：代理扩展、兼容开关及验收边界。
- [开发与验收参考](docs/开发与验收参考.md)：原README完整内容，含历史开发步骤和验收记录。
- [API契约](contracts/v1/openapi.json)：接口和请求响应结构。

当前已完成本地集成验证；真实支付渠道和生产部署不在本次验收范围内。
