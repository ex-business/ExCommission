# ExCommission

Go 模块化佣金分润系统，业务标准 V1。按顺序实施，未完成的能力不得宣称可用。

## 本地环境

使用 Docker Compose 和 MySQL 8.4。复制环境配置后填写本地专用密码：

```sh
cp .env.example .env
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
