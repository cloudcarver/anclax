"use strict";

const assert = require("node:assert/strict");
const { test } = require("node:test");
const { TaskClient } = require("./tasks.cjs");

function database(...responses) {
  return {
    calls: [],
    async query(text, values) {
      this.calls.push({ text, values });
      assert(responses.length > 0, "unexpected query");
      const response = responses.shift();
      if (response instanceof Error) throw response;
      return response;
    },
  };
}

const row = (id) => ({ rows: [{ id }], rowCount: 1 });
const empty = () => ({ rows: [], rowCount: 0 });

test("wire payload, attributes and scheduling columns match the Go store", async () => {
  const db = database(empty(), row(42));
  const payload = { userID: 7, nested: [true, null, "中文"] };
  const attributes = { labels: ["old"], retryPolicy: { interval: "1s", maxAttempts: 3 } };
  const override = { labels: [], tags: ["tenant:1"], serialKey: "order:1", serialID: 2,
    priority: 7, weight: 3, releaseUniqueTagOnTerminal: true };
  const startedAt = new Date("2030-01-01T00:00:00Z");
  const id = await new TaskClient(db).enqueue("sendEmail", payload,
    { uniqueTag: "unique'--", parentTaskId: 1, startedAt, attributes: override },
    { attributes, delayMs: 60000 });
  assert.equal(id, 42);
  const { text, values } = db.calls[1];
  assert(text.startsWith("INSERT INTO anclax.tasks"));
  assert.deepEqual(JSON.parse(values[1]), { type: "sendEmail", payload });
  assert.deepEqual(JSON.parse(values[0]), { ...attributes, ...override });
  assert.deepEqual(values.slice(2), ["pending", startedAt, "unique'--", 1, "order:1", 2, 7, 3]);
  assert.deepEqual(attributes.labels, ["old"]);
});

test("delay overrides the spec and default attributes are explicit", async () => {
  const db = database(row(1), row(2), row(3));
  const client = new TaskClient(db);
  const before = Date.now();
  await client.enqueue("work", {}, { delayMs: 5000 }, { delayMs: 300000 });
  const after = Date.now();
  assert(db.calls[0].values[3].getTime() >= before + 5000);
  assert(db.calls[0].values[3].getTime() <= after + 5000);
  assert.deepEqual(JSON.parse(db.calls[0].values[0]), { priority: 0, weight: 1 });
  await client.enqueue("work", {}, {}, { delayMs: 300000 });
  assert(db.calls[1].values[3].getTime() >= before + 300000);
  await client.enqueue("work", {});
  assert.equal(db.calls[2].values[3], null);
});

test("deduplication sees concurrent commits and retries released tags", async () => {
  for (const [responses, queries] of [
    [[row(9)], ["lookup"]],
    [[empty(), empty(), row(9)], ["lookup", "insert", "lookup"]],
    [[empty(), empty(), empty(), row(9)], ["lookup", "insert", "lookup", "insert"]],
  ]) {
    const db = database(...responses);
    assert.equal(await new TaskClient(db).enqueue("work", {}, { uniqueTag: "tag" }), 9);
    assert.deepEqual(db.calls.map((call) => call.text.startsWith("INSERT INTO anclax.tasks") ? "insert" : "lookup"), queries);
  }
});

test("database errors propagate without retrying a failed transaction", async () => {
  const error = new Error("serialization failure");
  const db = database(error);
  await assert.rejects(new TaskClient(db).enqueue("work", {}), (caught) => caught === error);
  assert.equal(db.calls.length, 1);
});

test("invalid input fails before SQL", async () => {
  for (const [name, payload, options] of [
    ["prefetchTasks", {}, {}], ["", {}, {}],
    ["work", {}, { uniqueTag: "anclax:system:x" }],
    ["work", {}, { attributes: { priority: -1 } }],
    ["work", {}, { attributes: { priority: true } }],
    ["work", {}, { attributes: { weight: 0 } }],
    ["work", {}, { attributes: { serialID: 1 } }],
    ["work", {}, { attributes: { serialKey: "" } }],
    ["work", {}, { attributes: { labels: "gpu" } }],
    ["work", {}, { attributes: { releaseUniqueTagOnTerminal: "true" } }],
    ["work", {}, { startedAt: new Date("invalid") }],
    ["work", {}, { startedAt: new Date(), delayMs: 0 }],
    ["cancelTaskOnWorker", {}, { attributes: { tags: ["business"] } }],
    ["work", { value: Infinity }, {}], ["work", { value: 2 ** 60 }, {}],
    ["work", () => {}, {}], ["work", Symbol("value"), {}],
    ["work", { toJSON: () => undefined }, {}],
  ]) {
    const db = database();
    await assert.rejects(new TaskClient(db).enqueue(name, payload, options));
    assert.deepEqual(db.calls, []);
  }
});

test("reads return rows or null and lightweight writes return affected counts", async () => {
  const task = { id: 1, attributes: { weight: 1 }, spec: { type: "work", payload: {} } };
  const event = { id: 2, spec: { type: "TaskError" } };
  const db = database({ rows: [task] }, empty(), { rows: [event] }, { rows: [], rowCount: 2 }, empty());
  const client = new TaskClient(db);
  assert.equal(await client.getTaskByID(1), task);
  assert.equal(await client.getTaskByUniqueTag("missing"), null);
  assert.equal(await client.getLastTaskErrorEvent(1), event);
  assert.equal(await client.updatePendingTaskPriorityByLabels(["gpu"], 3), 2);
  assert.equal(await client.updatePendingTaskWeightByLabels([], 2), 0);
  assert.deepEqual(db.calls[3].values, [3, true, ["gpu"]]);
  assert.deepEqual(db.calls[4].values, [2, false, []]);
});
