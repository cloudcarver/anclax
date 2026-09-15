# 按 task tag 限制全局并发

迁移 16 引入状态式预取：system 任务先为业务任务申请全部配额并设置 `ready`，Worker 再批量接管为 `running`，不重复扫描 pending 或申请 tag 名额。`ready` 占用配额，默认预留两秒，超时、取消、暂停或调度属性变化时原子撤销。system 任务独立领取，TaskStore 不接受其 tag、serial 或自定义 weight 配置。完整状态机、升级和回滚见[预取设计](ready-task-prefetch.md)。

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
| `RemoveTagConcurrencyLimit(ctx, tag)` | 恢复无限制，清除该 tag 的 permit 和计数，可重复调用。 |
| `GetTagConcurrency(ctx, tag)` | 返回 `Tag`、可空的 `MaxConcurrency`、`InUse`。无限额 tag 的占用为 0。 |
| `ListTagConcurrencyLimits(ctx, afterTag, pageSize)` | 按 tag 排序分页查询已配置规则，页大小 1–1000；首次游标为空，后续传上一页最后一个 tag。 |

没有配置规则的 tag 不限制并发。给已有 tag 启用限制时，已领取的任务也计入占用。降低上限不会中断运行中的任务，等待占用回落后再放行；提高或移除上限会唤醒等待任务，不需要广播 Worker 配置。

## 名额生命周期

- 自动领取、strict 任务和手动 `RunTask` 都检查 tag 限制，同时保留本地并发、labels、定时和 serial 约束。
- 自动准入在同一事务中取得全部 tag 名额并建立 ready 预留；Worker 接管时绑定执行租约并增加一次 attempts。手动 pending 领取仍合并这两步。无法运行的任务保持 pending，不增加 attempts，也不占住部分 tag。
- 占用从 ready 准入或手动领取持续到收尾事务提交。完成、失败、重试等待、下一次 cron 调度和 deferral 都在持久化结果的同一事务中释放名额。
- 执行中的任务在暂停或取消请求时不立即释放名额；尚未执行的 ready 预留则立即撤销。执行中的名额在执行器退出并完成收尾，或租约过期回收后释放。Resume 虽然使旧版本失效，但会保留旧租约的占用直到回收。
- 修改 tags 影响下一次尝试；本次尝试按领取时持久化的 tag 集合释放，避免改 tag 后计数泄漏或扣错。
- 框架内置的配置、暂停、取消及广播任务绕过业务 tag 限制，保证上限为 0 时控制任务仍能推进。

新租约记录数据库中的绝对过期时间和领取者的 TTL，短 TTL 的 Worker 不会提前回收长 TTL Worker 的任务。续租只更新任务行，不写共享 tag 计数。回收、续租和收尾争用同一任务行锁；释放只计算实际删除的 permit，旧 owner/version 无法影响新尝试。

限制的是数据库认可的有效执行租约。执行器仍需响应 context 取消，并让外部副作用支持幂等；断网或不配合取消的执行器可能在租约过期后继续执行。参见[租约说明](async-task-worker-lease.md)。

## 拉取性能

数据库分别维护任务与 tag 的映射 `task_tags`、配置 `task_tag_limits`、可独立占用的名额 `task_tag_slots`。`task_tag_concurrency` 和 `task_tag_permits` 现在是只读视图，提供占用量与任务归属；任务不再更新共享 `in_use`。映射只保留 pending/ready/running/paused 或仍持有租约的任务。任务进入终态且释放租约时，在同一事务中删除映射；原始 `attributes.tags` 保留，历史查询不受影响。任务恢复到可执行状态时会重建映射。配置好的 tag 限制继续保留，供未来任务使用。

无限额 tag 只保留任务元数据：普通任务不会为它创建共享注册行或 permit，不获取对应 tag 锁，也不更新共享计数。每个 ready 预留或持有执行租约的任务用 `lease_tags` 保存去重后的本次领取 tag 快照，修改 attributes 不会改变它，下一次尝试才替换快照。新增或重新启用限额时，在发布限额的同一事务中按快照回填名额占用；仍持有租约、等待回收的 paused/cancelled 尝试也计入。移除限额则删除名额，占用量变为零。名额存储量与配置容量成正比，若已有占用更多则还需保存超额名额；很大的有限配额会产生相应的配置和存储成本。

限额配置通过 **before-statement** 触发器先获取任务表的 `EXCLUSIVE` 锁，再锁定配置行，将回填与任务领取、入队、更新、收尾、续期串行化。普通任务事务不获取这个排他表锁。配置应使用短独立事务，在获取其他行锁之前修改限额；大量活跃租约的回填可能暂停调度和续期，需要预留时间。领取和限额配置要求 `READ COMMITTED`，即框架默认隔离级别；PostgreSQL 等价的 `READ UNCOMMITTED` 也可用。实现会拒绝持有旧快照的隔离级别，避免漏算并发领取或漏读新配置。

领取时对空闲名额获取独立的非阻塞 advisory lock，再用新 READ COMMITTED 快照确认仍为空闲。多 tag 分配失败会回滚该候选已经获取的名额和锁。完成和过期回收只释放本任务持有的名额，不获取分配锁，也不更新其他等待任务。新增或降低限额造成超额占用时，多余名额标记为退役；此时领取额外使用非阻塞的 tag 级超额保护和即时占用检查，释放仍相互独立。详见[名额协议](task-admission-design.md)。绕过协议直接写名额状态不受支持。

自动 Worker 每次只有一个 ready 批量领取查询在途，默认最多领取 32 条。system 任务负责按受限 tag 和 labels 分组，从 pending 选择候选、申请名额并设置 ready；Worker 接管时不重复检查 tag 或 serial 历史。权重组、labels 和 strict 上限继续生效。

ready 预留默认两秒，占用配额直到接管、撤销或回收。窗口按在线 Worker 容量限制，全局最多保留 4,096 条 ready，执行中任务另行统计。容量释放后下一轮 system 调度可补充任务。完整恢复和升级语义见[预取设计](ready-task-prefetch.md)。

预取 system 任务长期运行，在同一次执行租约内循环处理有界批次，每批使用独立短事务，空闲时在事务外等待 20 ms。Worker 预留两个控制槽位，让其他 system 命令持续执行。无有限额 tag、无 serial key 且无旧执行租约的任务批量更新为 ready；保留无限额 tag 快照供新增配额时回填。受限任务和过期租约继续走完整申请与释放路径。

调度指标包括 `anclax_task_scheduler_duration_seconds{operation}`、`anclax_task_scheduler_errors_total{operation,sqlstate}`、`anclax_task_claim_batch_size`（包括空批次）、`anclax_task_finalize_retries_total{sqlstate}`、`anclax_worker_task_phases{phase}`，配合已有租约续期指标，区分领取进展、空领取、收尾重试和最终错误。

## 已入库任务与升级

迁移 `0014_task_tag_concurrency` 最初引入 tag 并发限制，保留已有任务的 ID、payload、attributes（包括重复 tags）、状态、attempts、业务调度时间和租约版本。tag 回填仅覆盖 `status IN ('pending', 'running', 'paused') OR locked_at IS NOT NULL`；已完成、失败、取消且无租约的历史任务不生成映射或 tag 注册记录。未配置 tag 规则前，已有任务不受新的并发限制。

升级时停止旧 Worker，应用迁移，再启动新 Worker。不能混跑新旧版本：旧 SQL 不获取 tag 名额，也不更新绝对过期时间。旧租约没有记录领取者 TTL，新 Worker 沿用 `locked_at + 当前配置 TTL` 回收旧租约；之后的新尝试都记录自己的 TTL。过滤回填减少了历史 tags 的展开和派生记录写入，但筛选任务、建立索引仍可能扫描任务表。迁移仍在单个事务内执行，DDL 锁持续到提交，大任务表应预留维护窗口。

迁移 `0015_task_admission` 用独立名额替换计数器，从已有 permit 初始化任务 tag 快照，移除无限额占用和持久等待字段，添加 serial 租约及队头索引。配置写入改用 `task_tag_limits`；旧计数表和 permit 表名成为只读视图，自定义 SQL 写入和 TRUNCATE 工具需要调整。必须先停旧 Worker，迁移后只启动新版。回滚到 14 会按任务快照重建无限额 permit，恢复旧表、等待字段和函数；原始 attributes 与限额保留。迁移 15 已随 PR #71 合入，已经应用旧草稿的开发数据库应先用对应草稿二进制回滚，再应用新版。

自定义 model 和 mocks 需要适配新增的生成查询及任务字段；业务 Runner/Executor 合约和数据库任务 JSON 不变。SQL 辅助函数是内部实现，执行任务使用 Worker，并保留领取与结果查询的事务边界。受限 tag 的 `InUse` 是已准入占用，包含 ready 预留和尚未回收的过期租约，不等于实时 goroutine 数量；无限额 tag 不统计这个数值。

回滚也需先停 Worker。15 回滚到 14 会保留限额并恢复旧计数协议；继续回滚到 13 才删除并发配置、映射索引和占用记录，保留原任务数据。

## 验证

已完成的[独立名额测试记录](async-task-slot-benchmark.md)包含共享计数版与独立名额版在 100/200 执行槽位下各三次交替对比，以及名额版重新运行的 200 轮 chaos。[历史 v1.4.1 对比](async-task-admission-benchmark.md)保留旧版 PostgreSQL 死锁日志。

```bash
go test -race -tags ut ./... -timeout=6m
ANCLAX_SMOKE_POSTGRES_IMAGE=postgres:17 go test -race -tags=smoke ./pkg/taskcore/e2e \
  -run 'TestTaskSlotAdmissionSmoke|TestTaskAdmission(Migration)?Smoke|TestTaskTagConcurrency(Migration)?Smoke|TestTaskLifecycle(Regressions|Migration)Smoke|TestBatchedTaskLeaseRenewalSmoke' -count=1
ANCLAX_TASKCORE_CHAOS_POSTGRES_IMAGE=postgres:17 \
  ANCLAX_TASKCORE_CHAOS_ITERATIONS=200 go test -tags=smoke ./pkg/taskcore/chaos \
  -run TestContainerizedTaskcoreChaosSmoke -count=1 -v -timeout=30m
```

容器 chaos 测试混合受限和无限制业务任务：约三分之一的批量任务带全局 tag（上限 3）和分组 tag（上限 2），按轮次轮换路由分组和暂停/取消场景，不消耗选择故障的随机数。其余任务继续使用 Worker 可用并发，不受这两个上限约束。测试跨数据库重启持久化名额分配，检查可分配名额范围及任务/租约归属，并检查恢复后全部名额归零；报告记录两类任务数量及全局、分组峰值。独立的 PostgreSQL smoke 测试覆盖限额耗尽和大量阻塞任务积压。

每次 chaos 运行先用执行器阻塞信号确认“满额等待 → 释放 → 继续执行”，再杀掉明确的租约持有者，或仅切断其数据库连接，验证指定受限任务发生接管。初始化重试任务不能满足这些恢复断言。领取矩阵覆盖 tags 与 strict、normal、strict fallback、手动和通用 SQL 领取，以及 serial、labels、定时条件的组合。`make test` 包含短 chaos；本地长跑可使用不同 seed 执行 `make chaos`，持续负载分位数通过 `make taskcore-perf` 测量。参见[测试覆盖与运行方式](async-task-testing.zh.md)。

迁移 `0016_ready_task_prefetch` 增加 ready/running 状态、准入分组和独立 system 调度。升级或回滚都需先停止 Worker；回滚会释放尚未接管的 ready 预留并恢复迁移 15 协议，保留执行租约。详见[升级说明](ready-task-prefetch.md#upgrade-and-integration)。

状态式预取的三组前后对比、无积压场景回退及最终 200 轮 chaos 见[预取测试报告](ready-task-prefetch-benchmark.md)。

长任务循环和简单任务批量准入的后续对比、200 轮 chaos 与修正后的场景验证见[长任务预取报告](long-task-prefetch-benchmark.md)。
