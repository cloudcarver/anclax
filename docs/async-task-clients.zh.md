# 生成 Python / Node.js 异步任务客户端

`anclax gen` 生成可独立使用的客户端源文件，直接向 Go worker 使用的 PostgreSQL 插入任务。生成文件包含参数类型、任务选项、SQL 和运行逻辑，下游直接引用本地代码，无需安装 Anclax 的 Python / npm 包。

生成代码支持 Python 3.11+、Node.js 18+，接收应用已有的 asyncpg / node-postgres 连接或连接池。如果应用尚未安装数据库驱动，只需安装相应驱动：

```sh
python -m pip install asyncpg
npm install pg
```

## 从 spec 生成客户端

完整示例位于 [`examples/sample`](../examples/sample)。在仓库根目录执行 `go run ./cmd/anclax gen examples/sample`，即可将客户端分别生成到 `examples/sample/sdks/python` 和 `examples/sample/sdks/nodejs`。

在 `anclax.yaml` 中加入 `task-client`，与 Go 的 `task-handler` 使用同一个 spec：

```yaml
task-client:
  - language: python
    path: api/tasks/tasks.yaml
    out: generated/tasks.py
  - language: nodejs
    path: api/tasks/tasks.yaml
    out: generated/tasks.mjs
```

执行 `anclax gen`。配置支持单个对象和数组；只配置 `task-client` 的项目无需 `go.mod`、sqlc 或 Wire。路径相对于生成命令的工作目录。CLI 内嵌运行时模板和 SQL，生成时只需 task spec 及引用的 schema；运行生成代码时无需 CLI 或原始 spec。

Python 生成一个 `.py` 文件，包含 `TypedDict` 参数类型、`TaskOptions`、`TaskClient` 及 `Client.run_<snake_case_task_name>` 方法。Node.js 生成一个 JavaScript 文件，包含 `TaskClient` 和 `Client.run<TaskName>`，并生成完整 API 的 TypeScript 声明：`.mjs` 对应 ESM / `.d.mts`，`.cjs` 对应 CommonJS / `.d.cts`，`.js` 对应 CommonJS / `.d.ts`。设置了 `"type": "module"` 的项目请选择 `.mjs` 或 `.cjs`。每份生成客户端均可独立复制或提交到下游仓库。

入队前由 Go 应用执行框架迁移。CLI 版本应与 Go worker 保持兼容；修改 spec 或升级客户端逻辑后重新运行 `anclax gen`，在下游仓库审查和提交生成代码的变更。

支持对象、必填／可选字段、嵌套对象、数组、字典、标量、枚举、nullable、`oneOf` / `anyOf` 和本地 `$ref`，包括已有的 `file.yaml#schemas/Name`；引用相对于所在文件解析。递归引用和 `allOf` 会明确报生成错误。时间、UUID 等 payload 字段采用 JSON 字符串。类型用于静态检查，不做运行时 JSON Schema 验证或填充参数默认值；Go 专属类型扩展不影响客户端。省略 `parameters` 时与 Go 一致，生成含 `taskID` 的事件参数；空 payload 请显式定义 `type: object, properties: {}`。

假设 spec 定义了 `sendEmail`，参数为 `userID`、`template`：

```python
from generated.tasks import Client, TaskOptions

# pool 为应用持有的 asyncpg.Pool。
task_id = await Client(pool).run_send_email(
    {"userID": 123, "template": "welcome"},
    options=TaskOptions(unique_tag="welcome:123", attributes={"tags": ["tenant:acme"]}),
)
```

```javascript
import { Client } from "./generated/tasks.mjs";

// pool 为应用持有的 pg.Pool。
const taskID = await new Client(pool).runSendEmail(
  { userID: 123, template: "welcome" },
  { uniqueTag: "welcome:123", attributes: { tags: ["tenant:acme"] } },
);
```

## 事务和属性覆盖

构造 `Client` 时传入业务事务的连接，业务写入与入队即可原子提交。客户端不自行创建、提交、回滚事务，也不关闭调用方连接。传 pool 时各 SQL 由 pool 分配连接，因此事务必须传入已获取的 connection。参见 [asyncpg](https://magicstack.github.io/asyncpg/current/usage.html#transactions) 和 [node-postgres 事务文档](https://node-postgres.com/features/transactions)。

```python
async with pool.acquire() as conn, conn.transaction():
    await conn.execute("INSERT INTO welcome_requests(user_id) VALUES ($1)", 123)
    await Client(conn).run_send_email({"userID": 123, "template": "welcome"})
```

```javascript
const conn = await pool.connect();
try {
  await conn.query("BEGIN");
  await conn.query("INSERT INTO welcome_requests(user_id) VALUES ($1)", [123]);
  await new Client(conn).runSendEmail({ userID: 123, template: "welcome" });
  await conn.query("COMMIT");
} catch (error) {
  await conn.query("ROLLBACK");
  throw error;
} finally {
  conn.release();
}
```

| 功能 | Python | Node.js |
| --- | --- | --- |
| 唯一标签去重 | `unique_tag` | `uniqueTag` |
| 父任务 | `parent_task_id` | `parentTaskId` |
| 指定执行时间 | `started_at`，带时区的 `datetime` | `startedAt`，`Date` |
| 覆盖 spec 延迟 | `delay`，`timedelta` | `delayMs`，毫秒数 |
| 覆盖任务属性 | `attributes` | `attributes` |

两种语言的 `attributes` 都使用 JSON 字段名：`timeout`、`retryPolicy`、`cronjob`、`labels`、`tags`、`priority`、`weight`、`serialKey`、`serialID`、`releaseUniqueTagOnTerminal`。属性按顶层合并；提供 `retryPolicy` 时整体替换该策略。指定执行时间覆盖 spec 延迟，但不能同时传显式延迟选项。延迟选项替换 spec 延迟；没有延迟时 `started_at` 为 null。与 Go 相同，单独配置 cron 不推迟首次执行，后续执行时间由 Go worker 计算。

入队返回 task ID。唯一标签已存在时返回原任务 ID，不修改原任务。并发插入由唯一约束仲裁，READ COMMITTED 下通过后续查询读取胜出的任务；更高隔离级别的序列化错误等数据库异常原样交给调用方。默认在终止后保留唯一标签，启用 `releaseUniqueTagOnTerminal` 后由 Go 生命周期逻辑释放。客户端拒绝 worker 专用的 `prefetchTasks` 和 `anclax:system:` 唯一标签。

使用 asyncpg 默认 JSON codec / node-postgres 默认类型解析。Payload 是普通 JSON，不是 base64。Python 支持完整整数；Node.js 拒绝超出安全整数范围的数值，大整数标识应在 schema 中定义为字符串。通过驱动配置或调用方超时控制数据库等待。动态任务可调用 `TaskClient.enqueue(...)`；生成的方法额外保留 spec 默认属性和参数类型。

## 额外的轻量操作

| Python | Node.js | 返回值 |
| --- | --- | --- |
| `get_task_by_id(id)` | `getTaskByID(id)` | 任务或 `None` / `null` |
| `get_task_by_unique_tag(tag)` | `getTaskByUniqueTag(tag)` | 任务或 `None` / `null` |
| `get_last_task_error_event(id)` | `getLastTaskErrorEvent(id)` | 最近错误或 `None` / `null` |
| `update_pending_task_priority_by_labels(labels, priority)` | `updatePendingTaskPriorityByLabels(labels, priority)` | 更新行数 |
| `update_pending_task_weight_by_labels(labels, weight)` | `updatePendingTaskWeightByLabels(labels, weight)` | 更新行数 |

查询使用数据库列名（`id`、`unique_tag` 等），`spec` 和 `attributes` 已解码。更新匹配任一输入 label；空数组选择无 label 的任务，只修改 `pending`，排除 `ready`、执行中和终态任务；调度列与 JSON 属性原子更新。客户端无扫描或重计算，批量 SQL 命中大量任务时数据库仍有更新开销。

暂停／取消、子任务级联、worker 中断、等待完成、cron 重算及调度继续由 Go 控制面负责，单独改状态不能完整实现这些语义。

## 开发验证

运行时模板位于 `pkg/codegen/task/client_templates`。构建 CLI 时内嵌 Go store 共用的 `sql/queries/tasks.sql`，生成客户端时直接取用这些查询，无需单独生成 SQL 或构建运行时包。

```sh
make test-task-clients
npm --prefix pkg/codegen/task/clienttests ci
ANCLAX_TASK_CLIENT_TEST_DSN=postgresql://postgres:postgres@localhost:5432/postgres make test-task-client-integration
```

单元测试在没有 Anclax 包的隔离目录中执行生成客户端。集成测试需要建库权限，会创建并删除随机测试数据库，应用真实迁移，检查生成的 TypeScript 类型，并验证事务回滚、并发去重、标签更新和 Go store / handler 兼容性，包括终态唯一标签释放。可以使用本地 PostgreSQL，不强制依赖 Docker；`clienttests` 中的私有 npm 配置仅用于测试依赖。

集成测试入口还会运行 [`examples/sample` 的端到端测试](../examples/sample/README.md#end-to-end-test)：通过 CLI 在新目录生成客户端，运行两种语言的示例脚本，再由真实 Go worker 消费任务直到完成。只验证这条路径时，运行 `ANCLAX_TASK_CLIENT_TEST_DSN=... make test-sample-e2e`。
