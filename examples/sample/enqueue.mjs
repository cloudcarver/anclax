import pg from "pg";
import { Client } from "./sdks/nodejs/tasks.mjs";

if (!process.env.DATABASE_URL) {
  throw new Error("DATABASE_URL is required");
}

const pool = new pg.Pool({ connectionString: process.env.DATABASE_URL });
try {
  const taskID = await new Client(pool).runSendEmail(
    { userID: 123, template: "welcome", variables: { name: "Alice" } },
    { uniqueTag: "sample:welcome:123" },
  );
  console.log(taskID);
} finally {
  await pool.end();
}
