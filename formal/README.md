# 异步任务调度器的形式化验证

这里包含调度协议的参数化证明、组合模型的有限检查、活性条件与反例，以及对实际
Engine 和 PostgreSQL 的实现检查。统一执行入口是 `make verify-scheduler`。
结果见 [RESULTS.md](RESULTS.md)，源码对应论证及可信边界见 [IMPLEMENTATION.md](IMPLEMENTATION.md)。

## 当前证明到了哪里

| 对象 | 已检查的性质 | 证据与边界 |
| --- | --- | --- |
| [任务租约](tasklease/README.md) | 旧尝试不能 finalize 新租约；控制状态保留；资源释放归属 | TLC 检查事务交错；TLAPS 证明无版本上限的任务局部协议 |
| [共享 tag 槽位](tagslots/README.md) | 多 tag 原子分配、部分回滚、缩容期间准入 | TLC 检查语句交错；TLAPS 证明单 tag 聚合协议，含任意多次配置、解除限额与回填 |
| [Worker 预算](workerbudget/README.md) | 批量预留、执行与 finalize 计数；独立控制容量 | TLAPS 参数化预算证明；TLC 检查周期；直接遍历实际 `Engine.Apply` |
| [跨层组合](composed/README.md) | 租约与槽位同步、领取合同、旧执行迟到、serial 排他、scheduler fence | TLC 同时检查上述组件；含两个 Worker 接管、多 tag 和反复配置变更 |
| [SQL 与策略](proofs/README.md) | 返回数量界限、strict 饱和、retry／cron 决策、续租截止时间引理 | TLAPS 条件化定理，加真实 SQL 参数矩阵及现有实现回归 |
| [活性与不保证的行为](progress/README.md) | 有限工作量最终完成／释放、本地停机 drain；无条件承诺的反例 | 组合模型的条件活性检查；TLAPS 自然数归纳；饥饿、永久阻塞和重复副作用反例 |

**这些结果还不是原始 Go／SQL 程序的端到端、机器检查的 refinement 证明。**
参数化定理针对明确列出的抽象协议；组合的全部交错目前仍用有限状态检查。
实现对应论证经过源码审查和直接执行检查，但未由一个 Go／SQL 语义证明器验证。
这条边界不会因为状态数或测试数增加而消失。

配额缩容需要区分存量和新增：存量任务可以暂时超过新的 tag 上限或 strict 上限，
释放后才逐渐回到新上限。相应性质是禁止违反当前规则的新增分配，而不是强制取消存量。
活性结论要求配置、可用性和公平性条件。普通任务无饥饿、任意 handler 都能退出、任意
外部副作用 exactly-once 都不是当前系统的无条件保证，已有可重复运行的反例。

## 复现

在仓库根目录运行：

```sh
make verify-scheduler # 源码映射审查、TLC、TLAPS、Go race／状态遍历、PostgreSQL
make formal           # 单独运行全部 TLC 检查与反例
make formal-proof     # 单独运行六组参数化证明及三个故障变体
```

`make formal` 需要 Python 3.8+ 和 Java（已在 Java 17 验证）。运行器首次下载固定
[TLA+ tools 1.7.4](https://github.com/tlaplus/tlaplus/releases/tag/v1.7.4)，校验 SHA-256，
缓存在 `.anclax/formal-tools/`。可用 `TLA2TOOLS_JAR=/path/to/tla2tools.jar` 指定同一版本。

`make formal-proof` 在 Linux x86_64 首次下载并校验固定
[TLAPS 1.5.0](https://github.com/tlaplus/tlapm/releases/tag/202210041448) 安装包，约 145 MiB，
仅安装到 `.anclax/formal-tools/`。已有工具或其他平台可设置
`TLAPM=/absolute/path/to/tlapm`，要求版本 1.5.0；此时使用本机安装的后端。
运行器不修改系统软件或 shell 配置。

完整入口还需要 Go 和 Docker，使用仓库已有的 PostgreSQL smoke harness，在本机 5499
端口运行临时 `anclax-pg-smoke` 容器。`implementation.json` 保存本次审查的实现散列，变化后统一入口会
要求重新审查映射；不要仅更新散列而跳过审查。散列检查本身不是证明。

```sh
python3 formal/check.py --model tasklease
python3 formal/check.py --model tagslots
python3 formal/check.py --model workerbudget
python3 formal/check.py --model composed --timeout 240
python3 formal/check.py --model progress
python3 formal/check.py --case NoOverflowGuard --timeout 180
python3 formal/prove.py --module LeaseProofs
go test -tags=formal ./pkg/taskcore/worker -run TestFormalEngineConformance -count=1
```

产物位于被 git 忽略的 `.anclax/formal/verification-*`、`scheduler-*`、`proofs-*`。
保留输入副本、日志、实际命令、工具与源码散列和 `summary.json`。组合模型报告也记录
导入模块的散列。统一报告链接每一阶段的证据，并确认指定 Go／PostgreSQL 测试确实运行。

TLC 正向检查必须完整结束且队列为空；故障变体和可达性检查必须以退出码 12 报告指定
不变量的违反。活性反例必须以退出码 13 违反配置中唯一指定的时序性质。
TLAPS 必须实际完成非零数量的证明义务，禁用缓存命中，并拒绝省略证明。
其故障变体必须在归纳步骤上失败。解析错误、超时、其他工具错误都不能作为通过依据。

## 仍未获得的证明

原始 Go／SQL 的机器检查 refinement、完整组合协议的参数化归纳、真实时钟／网络／锁
管理器的全部行为、任意标签重路由或动态 serial 重排、迁移每一中间状态、事件与 hook
所有副作用，尚无完整演绎证明。有关路径有实现测试或局部定理时，报告会分别说明。
Go 的整数与 cycle ID 有限，数学自然数定理不能直接消除实现的溢出边界。

继续改动代码时，应同时审查实现映射和模型；仅重跑旧模型不能发现所有新增行为。
