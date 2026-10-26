"use strict";

const assert = require("node:assert/strict");
const path = require("node:path");
const { Pool } = require("pg");
const { Client } = require(path.join(process.env.ANCLAX_TASK_CLIENT_TEST_OUTPUT, "tasks.cjs"));
const { Client: FrameworkClient } = require(path.join(process.env.ANCLAX_TASK_CLIENT_TEST_OUTPUT, "framework.cjs"));

async function main() {
  const pool = new Pool({ connectionString: process.env.ANCLAX_TASK_CLIENT_TEST_DSN, max: 8 });
  try {
    const client = new Client(pool);
    const payload = { userID: 7, template: "welcome", recipients: [{ email: "a@example.test" }] };
    const id = await client.runSendEmail(payload, { uniqueTag: "node-defaults", parentTaskId: 123,
      attributes: { serialKey: "order:2", serialID: 2, weight: 3 } });
    const task = await client.getTaskByID(id);
    assert.deepEqual(task.spec, { type: "sendEmail", payload });
    assert.deepEqual(task.attributes.retryPolicy, { interval: "5s", maxAttempts: 3 });
    assert.equal(task.attributes.releaseUniqueTagOnTerminal, true);
    assert.equal(task.priority, 2147483647);
    assert.equal(task.weight, 3);
    assert.equal(task.serial_key, "order:2");
    assert.equal(task.parent_task_id, 123);
    assert(task.started_at instanceof Date);
    assert(task.admission_group_id !== null);
    const tags = await pool.query("SELECT tag FROM anclax.task_tags WHERE task_id=$1", [id]);
    assert.deepEqual(tags.rows, [{ tag: "welcome" }]);

    const ids = await Promise.all(Array.from({ length: 32 }, () => client.runNoParams({}, { uniqueTag: "node-race" })));
    assert.equal(new Set(ids).size, 1);
    const count = await pool.query("SELECT count(*)::int AS count FROM anclax.tasks WHERE unique_tag='node-race'");
    assert.equal(count.rows[0].count, 1);

    const conn = await pool.connect();
    try {
      await conn.query("BEGIN");
      await conn.query("INSERT INTO client_business VALUES (2)");
      await new Client(conn).runNoParams({}, { uniqueTag: "node-rollback" });
      await conn.query("ROLLBACK");
    } finally {
      conn.release();
    }
    assert.equal((await pool.query("SELECT count(*)::int AS count FROM client_business WHERE id=2")).rows[0].count, 0);
    assert.equal(await client.getTaskByUniqueTag("node-rollback"), null);
    assert.equal(await client.getTaskByID(-1), null);
    assert.equal(await client.getLastTaskErrorEvent(id), null);
    for (const [message, age] of [["old", 10], ["new", 0]]) {
      await pool.query("INSERT INTO anclax.events(spec,created_at) VALUES ($1, now()-$2*interval '1 second')",
        [{ type: "TaskError", taskError: { taskID: id, error: message } }, age]);
    }
    assert.equal((await client.getLastTaskErrorEvent(id)).spec.taskError.error, "new");

    const pending = await client.runNoParams({}, { attributes: { labels: ["light-node", "other"] } });
    for (const status of ["ready", "completed"]) {
      const excluded = await client.runNoParams({}, { attributes: { labels: ["light-node"] } });
      await pool.query("UPDATE anclax.tasks SET status=$1 WHERE id=$2", [status, excluded]);
    }
    assert.equal(await client.updatePendingTaskPriorityByLabels(["light-node", "absent"], 8), 1);
    assert.equal(await client.updatePendingTaskWeightByLabels(["light-node"], 4), 1);
    const changed = await client.getTaskByID(pending);
    assert.equal(changed.priority, 8);
    assert.equal(changed.attributes.priority, 8);
    assert.equal(changed.weight, 4);
    assert.equal(changed.attributes.weight, 4);
    await client.updatePendingTaskWeightByLabels([], 2);
    assert.equal((await client.getTaskByID(ids[0])).weight, 2);

    await new FrameworkClient(pool).runStressProbe({ jobID: 42, sleepMs: 0, group: "node" },
      { uniqueTag: "node-go", attributes: { releaseUniqueTagOnTerminal: true } });
    const python = await client.getTaskByUniqueTag("python-go");
    assert.equal(python.spec.payload.group, "python");
    console.log("Node.js PostgreSQL integration passed");
  } finally {
    await pool.end();
  }
}

main().catch((error) => { console.error(error); process.exitCode = 1; });
