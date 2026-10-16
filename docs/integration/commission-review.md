# 佣金审核列表与批量派发

本扩展提供可操作审核读模型、批量独立审批及派发草稿，不改写计算目标或历史资金。前端必须区分结算批准、本次计划金额和渠道实际到账。

GET /v1/commission-reviews 按 approval_status、agent_id、currency、period 筛选，limit=1..100，使用 after_id 作为同筛选游标；按approval_id稳定升序。返回审核主体/修订/摘要、结算修订/金额/状态、审批备注。GET /v1/payout-orders 按statement_id/status筛选，按instruction_id升序分页；GET /v1/payout-orders/{id}查看草稿、备注、修订、付款引用与错误码。所有查询绑定服务端作用域，不能跨租户。

POST /v1/commission-review-batches body={items:[{approval_id,decision,expected_revision,object_hash,reason,remark?}]}，最多100项、ID不重复。只允许STATEMENT，decision=APPROVE或REJECT（未批准时取消派发可用REJECT）。每项重新鉴权、禁止自审并检查原修订和摘要；返回每项http_status/data/error_code，不因一项失败回滚其他项。单笔沿用原/v1/approvals/{id}/decisions，新增可选remark（最多1000字），原请求指纹兼容。备注随审批结果同事务追加保存，不覆盖原审核证据。

POST /v1/payout-orders body={statement_id,amount:{currency,minor_units},channel_id,expected_statement_revision,remark}，只允许已批准且有效的结算，本次金额为正整数最小单位，不能超过当前可付余额或渠道/方案限额。生成DRAFT，不占用资金、不联系渠道；同一结算同一时刻只保留一张活动草稿/队列指令。

PUT /v1/payout-orders/{id} body={expected_revision,amount,remark}仅允许DRAFT，币种不变，保存修订及操作人。金额是本次派发额，减少不是扣除剩余佣金，增加不得超过批准剩余金额。原权益与结算总额不修改。

POST /v1/payout-order-batches body={action:RELEASE|CANCEL,items:[{instruction_id,expected_revision,reason}]}，1至100项，独立报告结果。RELEASE令DRAFT变QUEUED，显式提交派发；CANCEL只允许DRAFT或尚未创建任何付款的BLOCKED，变CANCELLED，不产生借贷。QUEUED之后不可撤销或编辑，不能用本地取消冒充渠道撤单。

后台独立任务每5秒取最多20条QUEUED，使用稳定指令键调用原payment-create。成功后链接原payment_id，变LINKED，解除活动指令占位；付款由原调度恢复/派发，UNKNOWN仅按原商户号查询。创建成功但链接前崩溃，下次重放稳定键只得到原付款。重复worker链接幂等，不重复占款。确定业务失败标记BLOCKED并保留错误码，可取消后重新准备；基础设施失败保留QUEUED重试。不同指令互不阻塞。

批量父键冻结完整清单；同键换金额、备注、对象或动作409。子键按父批次和资源派生，不使用客户端提交任意子键。每项命令审计、备注和状态持久化；重试每项重新鉴权，不能缓存整批结果绕过撤权。批量结果不代表银行到账。

审核查询和审核操作分别使用statements:read/statements:approve；派发查询与维护分别使用payments:read/payments:write。操作员不能通过批量入口自审，也不能提高批准权益。没有真实支付渠道时只验收沙箱。
