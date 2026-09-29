# 实现对应论证与可信边界

本文件是对当前 Go／SQL 的**人工步骤对应论证**。TLAPS 检查的是旁边的协议定理，
TLC 检查的是有限模型，Go／PostgreSQL 检查直接执行真实实现。三者互相补充，但目前
没有工具证明原始源码的每个可观察步骤都属于模型允许的步骤，即完整 source refinement。

[implementation.json](implementation.json) 标识所审查的实现、规格和执行检查。
`audit.py` 拒绝未经重新审查的源码变化；它不解析源码语义，不提供证明证书。

## 对应关系使用的基础合同

- Engine 的 `Apply` 由同一个事件循环串行调用；外部调用者遵守其单所有者访问合同。
- 使用框架的受支持领取／控制／finalize 入口；任意直接 SQL 可绕过协议，不在结论内。
- PostgreSQL 保证事务原子性、主键唯一性、行锁排他、savepoint 回滚及事务级 advisory
  lock。配置和资源分配使用实现要求的 READ COMMITTED 语义。
- 成功提交的写入被视为一个可见步骤；提交前读／锁／私有槽位写入可以互相交错。
  失败回滚不发布私有写入。提交确认丢失允许数据库已提交、调用者仍报告失败。
- 数学整数用于参数化证明；实际计数、版本、ID、时间计算需处于实现可表示的范围。
  Go 编译器／运行时、数据库、网络栈、时钟、cron 解析库与证明后端没有在此重新证明。

数据库基础语义参见 PostgreSQL 的[事务隔离](https://www.postgresql.org/docs/current/transaction-iso.html)
和[锁语义](https://www.postgresql.org/docs/current/explicit-locking.html)。读已提交事务不能
整体视为可串行化事务，所以槽位模型保留了 statement 之间的空隙。

## SQL 领取合同到 Worker 预算

[task_batch.sql](../sql/queries/task_batch.sql) 的两个候选集合分别要求 `priority > 0`
和 `priority = 0`，故互不相交。每个候选由唯一任务行产生，标签／group 过滤和
`SKIP LOCKED` 只能减少可选数量。严格候选先限制到 strictSlots，普通候选先限制到
batchSize，合并后再按 priority 排序并施加总 batchSize 限制。

记过滤并成功取得锁后的可用数量为 A、B，请求总量为 n、strict 预留为 s，且
`0 <= s <= n`。返回 strict 数为 `min(min(A,s),n)`，普通数不超过剩余总 LIMIT。
`AdmissionProofs!BatchQueryBounds` 证明结果总数不超过 n、strict 数不超过 s。
SQL UPDATE 每个主键至多返回一次，ready → running 保持 lease_version 和资源预留。
任务行锁阻止候选被并发改写后再以旧身份发布。

`ModelPort.ClaimBatch` 验证请求范围，在事务成功后才返回任务；事务错误返回空结果和
错误。`onClaimBatchResult` 先按 CycleID 排除旧回复，再去掉原预留并按实际任务创建
周期。因此返回合同正是预算定理需要的 `n<=reserved`、`strict<=strictReserved`。

**实际检查：** [SQL 矩阵](../pkg/taskcore/e2e/formal_contract_smoke_test.go) 执行 sqlc
生成的查询，共 28 组 batch／strict／labels 组合，检查非空预期数量、唯一 ID、租约版本
接管与 tag 资源保持；现有 admission smoke 还检查并发 batch、锁竞争和 serial 队首。
矩阵不是 SQL 语义证明，只是上述对应的独立执行证据。

## Engine 的状态投影和每类变化

把 `e.cycles` 中每个非 control 周期计入业务，LaneStrict 计入 strict 子集；control
独立计数。所有 phase 都计入，包括 claim 和 finalize。`e.batch.BatchSize`／StrictSlots
分别加到对应预留。这个投影给出 Worker 模型中的预算等式。

| 实现分支 | 对应动作与保持理由 |
| --- | --- |
| 构造 Engine | 所有周期与预留为空；非正业务容量规范化为 1，control 不小于 0 |
| `onPollTick`、`admitRequests`、control poll | 容量守卫通过后才创建周期、增加对应计数；控制独立；停止后不创建新周期 |
| `claimBatch` | min／clamp 公式满足 `BatchSizing`、`StrictSizing`，查询之前预留 |
| 批量结果 | 上述 SQL 合同加 `BatchBudgetComposition`；逐个创建周期等价于一次加入返回数量 |
| strict 为空后 fallback | 周期和预留保持；normal 结果为普通任务时只释放 strict 计数 |
| execute 返回 | 周期从执行变为 finalizing，预算不变 |
| `finishCycleResult` | 周期存在才按其 lane 减计数并删除；再次处理同一已删除周期为停顿 |
| 配置／通知／心跳 | cap 和轮转信息可更新，既有计数保留；无有效配置时不改变预算 |
| Stop | 拒绝新周期／批次，取消等待请求；已有领取、执行和 finalize 仍可返回 |

手动请求循环一次处理一个请求：或者留在队列，或者经过容量守卫加入一个周期，或者
直接返回完成命令。因此每次迭代保持预算；命令列表追加本身不修改预算。batch 回复
循环先去掉预留，再加入合同内的有限返回集合。CycleID 唯一性依赖单调分配且不回绕。

**实际检查：** [有界遍历](../pkg/taskcore/worker/formal_conformance_test.go) 调用真实
`Engine.Apply`，遍历 batch=0／2、strict=0／50／100、容量 2、control 容量 1 的六种配置，
深度至多 7、保留 ID 至多 3。状态键包含完整可变决策字段，不能只用 Snapshot 合并状态。
检查器独立从周期与 batch 计算预算，并检查旧周期结束重放、错误 batch 身份、停机
后的新准入和工作量增加。生成的是合法数量回复、空回复及控制／配置事件；错误返回的
全部组合由其他单测补充，没有声称此有限遍历覆盖任意容量和任意执行长度。

停机后 `onClaimStrictResult` 可以继续同一周期的 normal fallback，剩余 group 探测
也可继续。这不是新准入。反之，停机前发出的 batch 回复创建执行周期，是兑现已有
预留，不能在回复时丢弃这些已经取得的数据库租约。

## 租约、共享资源与 serial

任务行锁是身份检查与提交之间的边界。`FinalizeTaskAttempt` 条件匹配 Worker 和版本；
通过后，同一任务的 resume／takeover 不能在提交前改变该身份。SQL CASE 保留已提交
的 paused／cancelled。释放触发器按 task_id 清理资源，所以另一任务的所有权不变。
`LeaseProofs` 证明这一局部协议及局部更新提升；`TaskLease` 还显式保留历史签发尝试。

共享 tag 分配先锁任务，再按稳定 tag 顺序取得独立槽位 guard；取得 guard 后用新的
条件 UPDATE 重新检查。私有写入不会发布给其他事务，候选失败回滚所有已取得部分。
所以成功提交可以按“每个所需 tag 恰好一份”组合，失败是对已提交状态的停顿。
最终释放不需要分配 guard，可在其他分配事务读取快照时独立推进。

缩容后 retired 占用尚存在时，每次新分配必须经过 overflow guard 和 fresh count。
这期间同一 tag 的其他准入无法增加用量；释放只能减少。若最后一个 retired 占用
消失，其他分配可以走普通槽位路径，但每个私有预留仍占唯一普通槽位，所以
`ordinary + private + guarded <= limit`。`QuotaProofs` 对该聚合协议归纳，`TagSlots`
检查具体语句交错；完整的二者无界 refinement 仍是人工论证。

配置屏障等待任务事务结束，从 durable lease_tags 重建占用；缩容保留存量，解除限额
删除槽位但不删除尝试快照，重新限额时回填。串行准入另有每个 serial key 的 guard，
检查 ready／有效租约占用和排序队首；ready 本身就是 serial 占用。

预取 scheduler 在每个批次开始时检查系统任务的 Worker、version、有效期和身份并持
任务行锁，因此旧进程不能在接管后继续开始准入。已开始且仍持锁的事务即使期间到期，
也不能与另一个成功接管同时提交。其准备好的 ready 资源不依赖 scheduler 继续存活。

上述跨层关系在组合模型中一起检查；标签属性变化、任意 serial 动态重排和手动 claim
全部组合没有被该模型穷尽。数据库测试提供更多路径的执行证据，不补成未写出的定理。

## 生命周期、续租和活性

`decideAttempt` 的优先顺序是 lost、控制状态、deferral／interruption、retry，再到
cron 和终态。`PolicyProofs` 证明纯决策的部分合同；completion／error event、hook 的
持久化和隔离仍依靠 handler 事务及单测／数据库回归，没有完整的副作用一致性定理。

续租查询带版本和有效期条件；leaseManager 以请求开始时间计算本地 deadline，迟到
回复不能按响应到达时间延长信任。时间引理只覆盖该不等式，批次拆分、互斥、取消和
最终等待以实际 race 测试和 PostgreSQL 续租测试对照。

停机 drain 需要 handler 和数据库调用最终返回，retry／fallback 最终离开停顿；否则
工作量可永久保持不变。有限任务完成还要求匹配 Worker、正配额、调度选择公平、配置
稳定以及任务最终允许结束。条件定理和反例见[活性说明](progress/README.md)。

失效租约不会强制停止旧 handler，资源限制针对协议所有权，不是任意残留线程或外部
系统的物理执行次数上界。框架仍要求业务按至少一次执行设计幂等性。
