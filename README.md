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

当前健康接口只证明进程已启动，未实现的业务 URL 返回 404。worker 尚未装配任务领取器。

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

已实现用户同步与结果查询、代理创建、关系迁移申请、独立审批、历史祖先分页查询。关系不压缩物理层数，游标绑定租户、应用、代理、时间与分页条件；并发互绑通过作用域锁防止成环。合同端口按半开有效区间读取且拒绝重叠，不允许合同覆盖佣金费率。等级和方案资格在下一阶段实现。
