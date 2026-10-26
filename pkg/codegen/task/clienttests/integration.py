"""Cross-language integration test against a disposable database.

Set ANCLAX_TASK_CLIENT_TEST_DSN to a PostgreSQL admin URI with CREATE DATABASE rights.
The harness creates and drops only its own randomly named database.
"""

import asyncio
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
from urllib.parse import urlsplit, urlunsplit
from uuid import uuid4

import asyncpg

ROOT = Path(__file__).resolve().parents[4]


def load_module(path):
    spec = importlib.util.spec_from_file_location(path.stem, path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


async def run_command(*args, env=None):
    proc = await asyncio.create_subprocess_exec(*args, cwd=ROOT, env=env)
    if await proc.wait() != 0:
        raise RuntimeError(f"command failed: {args[0]}")


async def python_checks(pool, tasks, framework):
    Client, TaskOptions, FrameworkClient = tasks.Client, tasks.TaskOptions, framework.Client
    client = Client(pool)
    payload = {"userID": 2**60, "template": "welcome", "recipients": [{"email": "a@example.test"}]}
    task_id = await client.run_send_email(payload, options=TaskOptions(unique_tag="python-defaults",
        parent_task_id=123, attributes={"serialKey": "order:1", "serialID": 2, "weight": 3}))
    task = await client.get_task_by_id(task_id)
    assert task["spec"] == {"type": "sendEmail", "payload": payload}
    assert task["attributes"]["retryPolicy"] == {"interval": "5s", "maxAttempts": 3}
    assert task["attributes"]["releaseUniqueTagOnTerminal"] is True
    assert task["priority"] == 2147483647 and task["weight"] == 3
    assert task["serial_key"] == "order:1" and task["serial_id"] == 2
    assert task["parent_task_id"] == 123 and task["started_at"] is not None
    assert task["admission_group_id"] is not None
    assert await pool.fetchval("SELECT tag FROM anclax.task_tags WHERE task_id=$1", task_id) == "welcome"

    ids = await asyncio.gather(*(client.run_no_params({}, options=TaskOptions(unique_tag="python-race")) for _ in range(32)))
    assert len(set(ids)) == 1
    assert await pool.fetchval("SELECT count(*) FROM anclax.tasks WHERE unique_tag='python-race'") == 1

    class Rollback(Exception):
        pass

    try:
        async with pool.acquire() as conn, conn.transaction():
            await conn.execute("INSERT INTO client_business VALUES (1)")
            await Client(conn).run_no_params({}, options=TaskOptions(unique_tag="python-rollback"))
            raise Rollback()
    except Rollback:
        pass
    assert await pool.fetchval("SELECT count(*) FROM client_business WHERE id=1") == 0
    assert await client.get_task_by_unique_tag("python-rollback") is None
    assert await client.get_task_by_id(-1) is None
    assert await client.get_last_task_error_event(task_id) is None
    for message, age in [("old", 10), ("new", 0)]:
        await pool.execute("INSERT INTO anclax.events(spec,created_at) VALUES ($1, now()-$2*interval '1 second')",
            json.dumps({"type": "TaskError", "taskError": {"taskID": task_id, "error": message}}), age)
    assert (await client.get_last_task_error_event(task_id))["spec"]["taskError"]["error"] == "new"

    pending = await client.run_no_params({}, options=TaskOptions(attributes={"labels": ["light-python", "other"]}))
    for status in ("ready", "completed"):
        excluded = await client.run_no_params({}, options=TaskOptions(attributes={"labels": ["light-python"]}))
        await pool.execute("UPDATE anclax.tasks SET status=$1 WHERE id=$2", status, excluded)
    assert await client.update_pending_task_priority_by_labels(["light-python", "absent"], 8) == 1
    assert await client.update_pending_task_weight_by_labels(["light-python"], 4) == 1
    changed = await client.get_task_by_id(pending)
    assert changed["attributes"]["priority"] == changed["priority"] == 8
    assert changed["attributes"]["weight"] == changed["weight"] == 4
    await client.update_pending_task_weight_by_labels([], 2)
    assert (await client.get_task_by_id(ids[0]))["weight"] == 2

    await FrameworkClient(pool).run_stress_probe({"jobID": 42, "sleepMs": 0, "group": "python"},
        options=TaskOptions(unique_tag="python-go", attributes={"releaseUniqueTagOnTerminal": True}))
    print("Python PostgreSQL integration passed", flush=True)


async def main():
    admin_dsn = os.environ.get("ANCLAX_TASK_CLIENT_TEST_DSN")
    if not admin_dsn:
        raise SystemExit("Set ANCLAX_TASK_CLIENT_TEST_DSN to a PostgreSQL admin URI")
    uri = urlsplit(admin_dsn)
    if uri.scheme not in ("postgres", "postgresql"):
        raise SystemExit("ANCLAX_TASK_CLIENT_TEST_DSN must be a PostgreSQL URI")
    database = "anclax_client_" + uuid4().hex
    dsn = urlunsplit(uri._replace(path="/" + database))
    admin = await asyncpg.connect(admin_dsn)
    try:
        await admin.execute(f'CREATE DATABASE "{database}"')
        try:
            async with asyncpg.create_pool(dsn, min_size=1, max_size=8) as pool:
                async with pool.acquire() as conn:
                    for migration in sorted((ROOT / "sql/migrations").glob("*.up.sql")):
                        await conn.execute(migration.read_text())
                    await conn.execute("CREATE TABLE client_business(id integer PRIMARY KEY)")
                with tempfile.TemporaryDirectory(prefix="anclax-task-clients-") as tmp:
                    tmp = Path(tmp)
                    config = {"task-client": [
                        {"language": language, "path": str(ROOT / source), "out": str(tmp / (name + extension))}
                        for source, name in [("pkg/codegen/task/testdata/clients/tasks.yaml", "tasks"), ("api/tasks/tasks.yaml", "framework")]
                        for language, extension in [("python", ".py"), ("nodejs", ".cjs")]
                    ]}
                    config_path = tmp / "anclax.json"
                    config_path.write_text(json.dumps(config))
                    await run_command("go", "run", "./cmd/anclax", "gen", "--config", str(config_path))
                    modules = tmp / "node_modules"
                    modules.mkdir()
                    dependencies = Path(os.environ.get("ANCLAX_TASK_CLIENT_NODE_MODULES", ROOT / "pkg/codegen/task/clienttests/node_modules"))
                    for name in ("pg", "@types"):
                        (modules / name).symlink_to(dependencies / name, target_is_directory=True)
                    env = {**os.environ, "ANCLAX_TASK_CLIENT_TEST_DSN": dsn, "ANCLAX_TASK_CLIENT_TEST_OUTPUT": str(tmp),
                           "NODE_PATH": str(dependencies)}
                    # Also check that the generated declarations accept
                    # real pg Pool/PoolClient types and reject incorrect task parameters.
                    (tmp / "types.cts").write_text((ROOT / "pkg/codegen/task/testdata/clients/types.cts").read_text())
                    await run_command("node", str(dependencies / "typescript/bin/tsc"), "--strict", "--noEmit",
                        "--target", "es2022", "--module", "node16", str(tmp / "types.cts"))
                    tasks = load_module(tmp / "tasks.py")
                    await python_checks(pool, tasks, load_module(tmp / "framework.py"))
                    await run_command("node", str(ROOT / "pkg/codegen/task/clienttests/integration.cjs"), env=env)
                    await run_command("go", "test", "-tags=taskclientintegration", "./pkg/codegen/task/clienttests", "-count=1", "-v", env=env)
                    go_task = await tasks.TaskClient(pool).get_task_by_unique_tag("from-go")
                    assert go_task["spec"]["payload"]["group"] == "go"
                    print("Cross-language PostgreSQL integration passed", flush=True)
        finally:
            await admin.execute(f'DROP DATABASE "{database}" WITH (FORCE)')
    finally:
        await admin.close()


if __name__ == "__main__":
    asyncio.run(main())
