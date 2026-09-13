# 按 task tag 限制全局并发

[English](async-task-tag-concurrency.md) | 中文

复用任务现有的 `tags`，为每个 tag 配置一个跨所有 Worker 的并发上限，作用域是同一个 Anclax 数据库。不增加 `concurrencyKey` 字段。

例如配置 `tenant:42 = 3`、`vendor:api = 10`。同时带这两个 tag 的任务必须在两者中各取得一个名额才能运行。计数分别覆盖所有带对应 tag 的任务，与任务的其他 tag 无关，不是只统计两个 tag 的交集。

## 使用

通过应用现有的 WorkerControlPlane 配置：

```go
controlPlane := app.GetWorkerControlPlane()
if err := controlPlane.SetTagConcurrencyLimit(ctx, "tenant:42", 3); err != nil {
    return err
}
if err := controlPlane.SetTagConcurrencyLimit(ctx, "vendor:api", 10); err != nil {
    return err
}
```

生成的 Runner 继续使用 `taskcore.WithTags([]string{"tenant:42", "vendor:api"})`；直接使用 TaskStore 时设置 `TaskAttributes.Tags`。tag 区分大小写，重复 tag 只占一个名额。labels 继续用于 Worker 路由和权重分组，`uniqueTag` 继续用于入队去重。

| 方法 | 行为 |
| --- | --- |
| `SetTagConcurrencyLimit(ctx, tag, max)` | 设置或替换全局上限；0 暂停新尝试，拒绝负数和空 tag。 |
| `RemoveTagConcurrencyLimit(ctx, tag)` | 恢复无限制，保留 tag 和当前占用，可重复调用。 |
| `GetTagConcurrency(ctx, tag)` | 返回 `Tag`、可空的 `MaxConcurrency`、`InUse`。未出现过的 tag 返回无限制、占用 0。 |
| `ListTagConcurrencyLimits(ctx, afterTag, pageSize)` | 按 tag 排序分页查询已配置规则，页大小 1–1000；首次游标为空，后续传上一页最后一个 tag。 |

没有配置规则的 tag 不限制并发。给已有 tag 启用限制时，已领取的任务也计入占用。降低上限不会中断运行中的任务，等待占用回落后再放行；提高或移除上限会唤醒等待任务，不需要广播 Worker 配置。

## 名额生命周期

- 自动领取、strict 任务和手动 `RunTask` 都检查 tag 限制，同时保留本地并发、labels、定时和 serial 约束。
- 所有 tag 名额与任务租约、owner、attempts、lease version 在同一事务中取得。无法运行的任务保持 pending，不增加 attempts，也不占住部分 tag。
- 占用从领取持续到收尾事务提交。完成、失败、重试等待、下一次 cron 调度和 deferral 都在持久化结果的同一事务中释放名额。
- 暂停或取消请求本身不释放名额，要等执行器退出并完成收尾，或租约过期后回收。Resume 虽然使旧版本失效，但会保留旧租约的占用直到回收。
- 修改 tags 影响下一次尝试；本次尝试按领取时持久化的 tag 集合释放，避免改 tag 后计数泄漏或扣错。
- 框架内置的配置、暂停、取消及广播任务绕过业务 tag 限制，保证上限为 0 时控制任务仍能推进。

新租约记录数据库中的绝对过期时间和领取者的 TTL，短 TTL 的 Worker 不会提前回收长 TTL Worker 的任务。续租只更新任务行，不写共享 tag 计数。回收、续租和收尾争用同一任务行锁；释放只计算实际删除的 permit，旧 owner/version 无法影响新尝试。

限制的是数据库认可的有效执行租约。执行器仍需响应 context 取消，并让外部副作用支持幂等；断网或不配合取消的执行器可能在租约过期后继续执行。参见[租约说明](async-task-worker-lease.md)。

## 拉取性能

数据库分别维护任务与 tag 的映射 `task_tags`、配置和计数 `task_tag_concurrency`、本次尝试的占用 `task_tag_permits`。映射只保留 pending/running/paused 或仍持有租约的任务。任务进入终态且释放租约时，在同一事务中删除映射；原始 `attributes.tags` 保留，历史查询不受影响。任务恢复到可执行状态时会重建映射。配置好的 tag 限制继续保留，供未来任务使用。

未配置上限的 tag 也统计占用，因此在线启用规则不需要扫描运行任务。代价是活跃任务的映射存储和带 tag 尝试的计数更新；不带 tag 的任务不获取 tag 计数锁。

一次业务领取最多检查 32 条合格候选，按需逐条加锁，成功领取一条便停止。tag 按固定顺序使用 `FOR NO KEY UPDATE SKIP LOCKED` 加锁；有竞争时不会拿着部分名额等待其他 tag。数据库函数在获得锁后读取当前计数。

因限制而等待的任务会移出部分就绪索引。新任务入队时若发现 tag 已满，会直接进入等待。释放和提高上限时，通过 tag、可运行时间的索引唤醒至多 64 条任务，并受可用名额约束。唤醒只是提示，真正领取时仍原子检查所有 tag。

Worker 在领取或心跳时执行有界维护，每个 Worker 最多每 250 ms 一次；每轮最多检查 64 个新租约、64 个旧格式租约和 64 个到期等待任务。阻塞 tag 仍满时，任务继续留在等待索引，不重新堵住就绪队列。锁竞争设置 100 ms 的重查提示，容量不足设置 5 s 提示。这是最早重查时间；积压和锁竞争会影响实际恢复延迟。

32 条候选限制约束的是 tag 检查工作量，不是所有 SQL 扫描量：labels、serial 队头、排序和跳过已锁行仍可能读取更多行。对已有大量就绪任务启用新规则后，会分批把阻塞任务移到等待队列。单个热点 tag、大量路由分组或等待组合仍需按实际负载压测，不保证不同 tag 组合之间的严格 FIFO 或公平性。

PostgreSQL smoke 测试构造 20,070 条等待任务和 1 条就绪任务，通过 `EXPLAIN (ANALYZE, BUFFERS)` 检查索引，并记录完整领取耗时。本地 PostgreSQL 17/Docker 测得约 2–3 ms；这是回归样例，不是生产环境吞吐或延迟承诺。

## 已入库任务与升级

迁移 `0014_task_tag_concurrency` 保留已有任务的 ID、payload、attributes（包括重复 tags）、状态、attempts、业务调度时间和租约版本。tag 回填仅覆盖 `status IN ('pending', 'running', 'paused') OR locked_at IS NOT NULL`；已完成、失败、取消且无租约的历史任务不生成映射或 tag 注册记录。仍计入所有尚有租约的业务尝试，包括 paused/cancelled。未配置 tag 规则前，已有任务不受新的并发限制。

升级时停止旧 Worker，应用迁移，再启动新 Worker。不能混跑新旧版本：旧 SQL 不获取 tag 名额，也不更新绝对过期时间。旧租约没有记录领取者 TTL，新 Worker 沿用 `locked_at + 当前配置 TTL` 回收旧租约；之后的新尝试都记录自己的 TTL。过滤回填减少了历史 tags 的展开和派生记录写入，但筛选任务、建立索引仍可能扫描任务表。迁移仍在单个事务内执行，DDL 锁持续到提交，大任务表应预留维护窗口。

自定义 model 和 mocks 需要适配新增的生成查询及任务字段；业务 Runner/Executor 合约和数据库任务 JSON 不变。SQL 辅助函数是内部实现，执行任务使用 Worker，并保留领取与结果查询的事务边界。`InUse` 是已领取的占用，包含尚未回收的过期租约，不等于实时 goroutine 数量。

回滚也需先停 Worker；down migration 保留任务数据，删除并发配置、映射索引和占用记录。

## 验证

```bash
go test -race -tags ut ./... -timeout=6m
ANCLAX_SMOKE_POSTGRES_IMAGE=postgres:17 go test -race -tags=smoke ./pkg/taskcore/e2e \
  -run 'TestTaskTagConcurrency(Migration)?Smoke|TestTaskLifecycle(Regressions|Migration)Smoke' -count=1
ANCLAX_TASKCORE_CHAOS_POSTGRES_IMAGE=postgres:17 \
  ANCLAX_TASKCORE_CHAOS_ITERATIONS=200 go test -tags=smoke ./pkg/taskcore/chaos \
  -run TestContainerizedTaskcoreChaosSmoke -count=1 -v -timeout=30m
```

容器 chaos 测试混合受限和无限制业务任务：约三分之一的批量任务带全局 tag（上限 3）和分组 tag（上限 2），按轮次轮换路由分组和暂停/取消场景，不消耗选择故障的随机数。其余任务继续使用 Worker 可用并发，不受这两个上限约束。测试跨数据库重启持久化每次计数增加，逐次检查上限和 permit/计数一致性，并检查恢复后全部名额归零；报告记录两类任务数量及全局、分组峰值。独立的 PostgreSQL smoke 测试覆盖限额耗尽和大量阻塞任务积压。
