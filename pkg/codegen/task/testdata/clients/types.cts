import { Pool } from "pg";
import { Client, SendEmailParameters, TaskClient } from "./tasks.cjs";

const pool = new Pool();
const client = new Client(pool);
const params: SendEmailParameters = { userID: 7, template: "welcome", recipients: [{ email: "a@example.test" }] };
void client.runSendEmail(params, { uniqueTag: "hello", attributes: { priority: 2 } });
void client.runNullable(null);
void pool.connect().then((conn) => new TaskClient(conn).enqueue("work", {}));
// @ts-expect-error required field
void client.runSendEmail({ template: "welcome" });
// @ts-expect-error enum value
void client.runSendEmail({ userID: 7, template: "invalid" });
// @ts-expect-error nested field
void client.runSendEmail({ userID: 7, template: "welcome", recipients: [{ email: 1 }] });
// @ts-expect-error attribute type
void client.runSendEmail(params, { attributes: { priority: "high" } });
