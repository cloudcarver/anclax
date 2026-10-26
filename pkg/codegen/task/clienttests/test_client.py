import asyncio
from collections import deque
from datetime import datetime, timedelta, timezone
import json
import unittest

from tasks import TaskClient, TaskOptions


class Database:
    def __init__(self, *responses):
        self.responses = deque(responses)
        self.calls = []

    async def fetchrow(self, query, *args):
        self.calls.append((query, args))
        response = self.responses.popleft()
        if isinstance(response, BaseException):
            raise response
        return response

    execute = fetchrow


class TaskClientTests(unittest.IsolatedAsyncioTestCase):
    async def test_wire_payload_and_scheduling_columns(self):
        db = Database(None, {"id": 42})
        payload = {"userID": 2**60, "nested": [True, None, "中文"]}
        attributes = {"labels": ["old"], "retryPolicy": {"interval": "1s", "maxAttempts": 3}}
        override = {"labels": [], "tags": ["tenant:1"], "serialKey": "order:1", "serialID": 2,
                    "priority": 7, "weight": 3, "releaseUniqueTagOnTerminal": True}
        started = datetime(2030, 1, 1, tzinfo=timezone.utc)
        task_id = await TaskClient(db).enqueue("sendEmail", payload, attributes=attributes, delay_seconds=60,
            options=TaskOptions(unique_tag="unique'--", parent_task_id=1, started_at=started, attributes=override))
        self.assertEqual(task_id, 42)
        query, args = db.calls[-1]
        self.assertTrue(query.startswith("INSERT INTO anclax.tasks"))
        self.assertEqual(json.loads(args[1]), {"type": "sendEmail", "payload": payload})
        self.assertEqual(json.loads(args[0]), {**attributes, **override})
        self.assertEqual(args[2:], ("pending", started, "unique'--", 1, "order:1", 2, 7, 3))
        self.assertEqual(attributes["labels"], ["old"])

    async def test_delay_and_defaults(self):
        db = Database({"id": 1}, {"id": 2}, {"id": 3})
        client = TaskClient(db)
        before = datetime.now(timezone.utc)
        await client.enqueue("work", {}, delay_seconds=300, options=TaskOptions(delay=timedelta(seconds=5)))
        after = datetime.now(timezone.utc)
        self.assertTrue(before + timedelta(seconds=5) <= db.calls[0][1][3] <= after + timedelta(seconds=5))
        self.assertEqual(json.loads(db.calls[0][1][0]), {"priority": 0, "weight": 1})
        await client.enqueue("work", {}, delay_seconds=300)
        self.assertGreaterEqual(db.calls[1][1][3], before + timedelta(seconds=300))
        await client.enqueue("work", {})
        self.assertIsNone(db.calls[2][1][3])

    async def test_unique_tag_conflicts_and_release_race(self):
        for responses, queries in [
            ([{"id": 9}], ["lookup"]),
            ([None, None, {"id": 9}], ["lookup", "insert", "lookup"]),
            ([None, None, None, {"id": 9}], ["lookup", "insert", "lookup", "insert"]),
        ]:
            with self.subTest(responses=responses):
                db = Database(*responses)
                self.assertEqual(await TaskClient(db).enqueue("work", {}, options=TaskOptions(unique_tag="tag")), 9)
                self.assertEqual(["insert" if query.startswith("INSERT INTO anclax.tasks") else "lookup"
                                  for query, _ in db.calls], queries)

    async def test_database_errors_and_cancellation_propagate(self):
        for error in [RuntimeError("serialization failure"), asyncio.CancelledError()]:
            db = Database(error)
            with self.assertRaises(type(error)) as caught:
                await TaskClient(db).enqueue("work", {})
            self.assertIs(caught.exception, error)
            self.assertEqual(len(db.calls), 1)

    async def test_invalid_input_fails_before_sql(self):
        cases = [
            ("prefetchTasks", {}, TaskOptions()),
            ("", {}, TaskOptions()),
            ("work", {}, TaskOptions(unique_tag="anclax:system:x")),
            ("work", {}, TaskOptions(attributes={"priority": -1})),
            ("work", {}, TaskOptions(attributes={"priority": True})),
            ("work", {}, TaskOptions(attributes={"weight": 0})),
            ("work", {}, TaskOptions(attributes={"serialID": 1})),
            ("work", {}, TaskOptions(attributes={"serialKey": ""})),
            ("work", {}, TaskOptions(attributes={"labels": "gpu"})),
            ("work", {}, TaskOptions(attributes={"releaseUniqueTagOnTerminal": "true"})),
            ("work", {}, TaskOptions(started_at=datetime(2030, 1, 1))),
            ("work", {}, TaskOptions(started_at=datetime.now(timezone.utc), delay=timedelta(0))),
            ("cancelTaskOnWorker", {}, TaskOptions(attributes={"tags": ["business"]})),
            ("work", {"number": float("nan")}, TaskOptions()),
        ]
        for name, payload, options in cases:
            with self.subTest(name=name, options=options):
                db = Database()
                with self.assertRaises((ValueError, TypeError)):
                    await TaskClient(db).enqueue(name, payload, options=options)
                self.assertEqual(db.calls, [])

    async def test_reads_and_lightweight_updates(self):
        db = Database({"id": 1, "spec": '{"type":"work","payload":{}}', "attributes": '{"weight":1}'},
                      None, {"id": 2, "spec": {"type": "TaskError"}}, "UPDATE 2", "UPDATE 0")
        client = TaskClient(db)
        self.assertEqual((await client.get_task_by_id(1))["attributes"], {"weight": 1})
        self.assertIsNone(await client.get_task_by_unique_tag("missing"))
        self.assertEqual((await client.get_last_task_error_event(1))["spec"], {"type": "TaskError"})
        self.assertEqual(await client.update_pending_task_priority_by_labels(["gpu"], 3), 2)
        self.assertEqual(await client.update_pending_task_weight_by_labels([], 2), 0)
        self.assertEqual(db.calls[-2][1], (3, True, ["gpu"]))
        self.assertEqual(db.calls[-1][1], (2, False, []))


if __name__ == "__main__":
    unittest.main()
