# 参数化协议证明

六个模块由 TLAPS 1.5.0 检查，`make formal-proof` 一次运行全部证明及故障变体。
单独运行示例：`python3 formal/prove.py --module LeaseProofs`。
工具安装与产物路径见[总览](../README.md)。

| 模块 | 证明义务 | 参数化结论 |
| --- | ---: | --- |
| [SchedulerProofs](SchedulerProofs.tla) | 16 | 任意业务／控制容量下，抽象预算协议保持计数和容量约束；batch／strict 公式满足边界 |
| [LeaseProofs](LeaseProofs.tla) | 20 | 无版本数量上限的任务局部协议保持资源账目、控制状态和 finalize fence；释放要求当前身份，过期租约不能续租；局部更新可提升到任意任务集合 |
| [AdmissionProofs](AdmissionProofs.tla) | 20 | SQL 候选过滤／排序／LIMIT 的计数合同、与预算的组合、strict 饱和、配置回填和续租时间的条件引理 |
| [QuotaProofs](QuotaProofs.tla) | 10 | 单 tag 聚合协议支持任意用量、任意多次缩容／扩容、解除限额和重新回填，保持“不新增超额”；原子多 tag 提交的计数引理 |
| [ProgressProofs](ProgressProofs.tla) | 52 | 任何自然数工作量，在只减少且公平推进时最终归零；领取／执行／finalize 的加权工作量满足下降条件 |
| [PolicyProofs](PolicyProofs.tla) | 12 | 取消／暂停／丢失租约的优先级、延后不消耗重试、有限重试阈值、fatal、cron 重置次数与成功完成事件条件 |

合计 **130 个证明义务**。这里的参数化针对各模块的数学协议，不等于全部源码或全部
组件组合已经得到无界证明。

## 关键不变量与前提

预算模型计数 `active + reserved <= Capacity`，strict 是业务子集，control 独立。
`Downgrade` 对 strict 预留领取到普通任务的情况只减少 strict 计数。execute 到 finalize
不归还容量，因此在计数模型中是停顿。

租约模型用 `held=0/1` 表达任务行锁；lease 的 `0/1/2` 分别表示无租约、有效、过期。
取得 finalize 锁时保存 Worker 和 epoch，所有改写身份的动作要求未持锁；时间仍可流逝。
resume 提升 fence 并保留旧资源，故资源 epoch 可以小于当前 fence。`Prepare` 和
`ClaimPending` 只表示成功的原子资源申请，失败申请为停顿；资源分配的证明在另一模块。

`QuotaProofs` 区分普通占用、retired 占用、普通私有预留和一个持 overflow guard 的预留。
核心加强不变量是：

```text
ordinary + private + guarded <= limit
retired > 0  => private = 0
retired > 0 AND guarded = 1 => ordinary + retired <= checked < limit
```

最后一个 retired 占用消失后，其他普通分配可以加入，安全性转而由普通槽位界限维持。
配置在分配事务结束后保留全部存量，按新限额分成普通和 retired；解除限额时保留持有者
总数，重新启用时回填。该模型假设私有预留已得到正确的互斥槽位，并把 guard／计数／
预留抽象成一步；真实多语句交错由 [TagSlots](../tagslots/README.md) 有限检查。
二者的抽象关系有人工论证，尚未机器检查为任意规模的 refinement 定理。

SQL 计数证明以过滤后的可锁候选数为输入，先应用两个 LIMIT，再根据 priority 顺序应用
总 LIMIT。唯一主键、lane 互斥和 SQL 标准 LIMIT 语义是对应前提。
`LocalRenewalDeadline` 在统一时间轴上比较请求开始与数据库语句开始；它没有证明
系统时钟漂移、跳变或 leaseManager 的整个并发实现。策略证明同样将 duration／cron
解析和下一时间计算作为库合同，不证明日历库本身。

## 活性

停止新准入后定义工作量 `3*claiming + 2*executing + finalizing`。批量预留被不超过它的
执行数量替换，execute 返回把权重 2 变成 1，finalize 返回归还最后 1，均严格下降。
fallback、失败重试、无关事件可以停顿。`EventualDrain` 用自然数归纳和显式弱公平性
证明最终归零，允许一次减少任意正数量，并不限于每步减一。

**公平下降是环境合同，未从任意 Go handler 或任意数据库故障中推导出来。** 归零表示
本地未完成命令清空，不直接保证所有 durable ready 资源已释放。相关条件及反例见
[活性说明](../progress/README.md)。ENABLED 展开和时序归纳采用 TLAPS 提供的证明机制，
可参阅维护者的[自然数终止证明示例](https://discuss.tlapl.us/msg04969.html)。

## 证明自检与可信边界

运行器在独立产物目录生成三个故障变体：去掉预算上限、去掉 finalize 版本条件、允许
retired 尚存在时绕过 overflow 序列化。要求对应 `InductiveInvariant` 明确失败。
解析错误、超时和其他失败不算通过；证明失败本身也不当作反例生成器。

每次使用 `--threads 1 --nofp`，禁止省略证明。SMT、PTL、证明管理器以及引用的标准
数学库属于可信工具；没有额外声称取得独立内核复核证书。数学自然数、抽象动作和
实际 Go／SQL 之间仍有[实现对应边界](../IMPLEMENTATION.md)。
