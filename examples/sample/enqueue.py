"""Enqueue a task using the generated Python client and an application-owned pool."""

import asyncio
import os

import asyncpg

from sdks.python.tasks import Client, TaskOptions


async def main() -> None:
    async with asyncpg.create_pool(os.environ["DATABASE_URL"]) as pool:
        task_id = await Client(pool).run_send_email(
            {"userID": 123, "template": "welcome", "variables": {"name": "Alice"}},
            options=TaskOptions(unique_tag="sample:welcome:123"),
        )
        print(task_id)


if __name__ == "__main__":
    asyncio.run(main())
