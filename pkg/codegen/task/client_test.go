package codegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateClients(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, language := range []string{"python", "nodejs"} {
		t.Run(language, func(t *testing.T) {
			dir := t.TempDir()
			ext := ".py"
			if language == "nodejs" {
				ext = ".cjs"
			}
			output := filepath.Join(dir, "tasks"+ext)
			if err := GenerateClient(root, language, "pkg/codegen/task/testdata/clients/tasks.yaml", output); err != nil {
				t.Fatal(err)
			}
			first, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if err := GenerateClient(root, language, "pkg/codegen/task/testdata/clients/tasks.yaml", output); err != nil {
				t.Fatal(err)
			}
			second, _ := os.ReadFile(output)
			if string(first) != string(second) {
				t.Fatal("generation is nondeterministic")
			}
			if language == "python" {
				python, err := exec.LookPath("python3")
				if err != nil {
					t.Skip("python3 is required to exercise the generated Python client")
				}
				cmd := exec.Command(python, "-S", "-c", `
import asyncio, typing
from tasks import Client, SendEmailParameters, OnFailureParameters, NullableParameters, TaskOptions
assert SendEmailParameters.__required_keys__ == {'userID', 'template'}
assert SendEmailParameters.__optional_keys__ == {'headers', 'recipients', 'x-key'}
assert OnFailureParameters.__required_keys__ == {'taskID'}
assert typing.get_type_hints(SendEmailParameters)['userID'] is int
class Capture(Client):
    async def enqueue(self, name, payload, **kw):
        self.request = name, payload, kw
        return 42
async def check():
    client = Capture(None)
    options = TaskOptions(unique_tag='hello')
    payload = {'userID': 7, 'template': 'welcome', 'recipients': [{'email': 'a@example.test'}]}
    assert await client.run_send_email(payload, options=options) == 42
    name, params, kw = client.request
    assert (name, params, kw['options']) == ('sendEmail', payload, options)
    assert kw['delay_seconds'] == 0.25
    assert kw['attributes'] == {'timeout': '1m', 'retryPolicy': {'interval': '5s', 'maxAttempts': 3},
        'cronjob': {'cronExpression': '0 * * * * *'}, 'labels': ['email'], 'tags': ['welcome'],
        'priority': 2147483647, 'releaseUniqueTagOnTerminal': True}
    assert await client.run_nullable(None) == 42
asyncio.run(check())
`)
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("generated Python client: %v\n%s", err, out)
				}
				runClientRuntimeTests(t, python, root, dir, "test_client.py", "-S", "test_client.py")
			} else {
				node, err := exec.LookPath("node")
				if err != nil {
					t.Skip("node is required to exercise the generated Node.js client")
				}
				if err := GenerateClient(root, language, "pkg/codegen/task/testdata/clients/tasks.yaml", filepath.Join(dir, "tasks.mjs")); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(node, "--input-type=module", "-e", `
import assert from 'node:assert/strict';
import { Client as CommonJS } from './tasks.cjs';
import { Client as ESM } from './tasks.mjs';
for (const Client of [CommonJS, ESM]) {
    const client = new Client(null);
    const payload = {userID: 7, template: 'welcome'};
    const options = {uniqueTag: 'hello'};
    client.enqueue = async (name, params, opts, defaults) => {
        assert.equal(name, 'sendEmail');
        assert.equal(params, payload);
        assert.equal(opts, options);
        assert.deepEqual(defaults, {delayMs: 250, attributes: {
            timeout: '1m', retryPolicy: {interval: '5s', maxAttempts: 3},
            cronjob: {cronExpression: '0 * * * * *'}, labels: ['email'], tags: ['welcome'],
            priority: 2147483647, releaseUniqueTagOnTerminal: true,
        }});
        return 42;
    };
    assert.equal(await client.runSendEmail(payload, options), 42);
    const direct = new Client({query: async (sql, values) => {
        assert(sql.startsWith('INSERT INTO anclax.tasks'));
        assert.deepEqual(JSON.parse(values[1]), {type: 'noParams', payload: {}});
        return {rows: [{id: 43}]};
    }});
    assert.equal(await direct.runNoParams({}), 43);
}
`)
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("generated Node.js client: %v\n%s", err, out)
				}
				declarations, err := os.ReadFile(filepath.Join(dir, "tasks.d.cts"))
				if err != nil {
					t.Fatal(err)
				}
				for _, want := range []string{`"userID": number`, `"recipients"?: Array<`, `"template": ("welcome" | "reminder")`, `runSendEmail(params: SendEmailParameters, options?: TaskOptions): Promise<number>`} {
					if !strings.Contains(string(declarations), want) {
						t.Errorf("missing %q in declarations", want)
					}
				}
				runClientRuntimeTests(t, node, root, dir, "client.test.cjs", "--test", "client.test.cjs")
			}
		})
	}
}

// Exercise the actual output in a directory containing no Anclax package,
// templates or source SQL. Python -S also excludes installed site packages.
func runClientRuntimeTests(t *testing.T, interpreter, root, dir, testFile string, args ...string) {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(root, "pkg/codegen/task/clienttests", testFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, testFile), source, 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(interpreter, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "NODE_PATH=", "PYTHONPATH=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("standalone client runtime tests: %v\n%s", err, out)
	}
}

func TestClientSQLUsesCanonicalQueries(t *testing.T) {
	queries, err := clientQueries()
	if err != nil {
		t.Fatal(err)
	}
	for name, query := range queries {
		if strings.Contains(query, "sqlc.") || strings.Contains(query, "-- name:") {
			t.Errorf("%s contains generator directives: %s", name, query)
		}
	}
	for _, field := range []string{"Priority", "Weight"} {
		query := queries["UpdatePendingTask"+field+"ByLabels"]
		for _, expected := range []string{"$1::int", "$2::bool", "$3::text[]"} {
			if !strings.Contains(query, expected) {
				t.Errorf("%s update missing parameter %s", field, expected)
			}
		}
	}
	if _, err := extractClientQueries(""); err == nil {
		t.Error("missing canonical queries must fail generation")
	}
	if _, err := extractClientQueries("-- name: CreateTask :one\nSELECT $1, sqlc.arg(name);"); err == nil || !strings.Contains(err.Error(), "mixes") {
		t.Errorf("ambiguous parameter numbering must fail generation, got %v", err)
	}
}

func TestClientGeneratorRejectsInvalidInput(t *testing.T) {
	for _, tc := range []struct{ name, spec, want string }{
		{"duplicate", "tasks:\n  - name: foo\n  - name: foo\n", "duplicate method"},
		{"collision", "tasks:\n  - name: sendEmail\n  - name: send_email\n", "duplicate method"},
		{"invalid name", "tasks:\n  - name: bad-name\n", "must be an identifier"},
		{"missing reference", "tasks:\n  - name: foo\n    parameters:\n      $ref: missing.yaml#/schemas/Foo\n", "resolve"},
		{"bad cron", "tasks:\n  - name: foo\n    cronjob: {}\n", "cronExpression"},
		{"recursive reference", "tasks:\n  - name: foo\n    parameters:\n      $ref: '#/schemas/Foo'\nschemas:\n  Foo:\n    type: object\n    properties:\n      child:\n        $ref: '#/schemas/Foo'\n", "recursive schema"},
		{"composition", "tasks:\n  - name: foo\n    parameters:\n      allOf: []\n", "allOf is not supported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "tasks.yaml"), []byte(tc.spec), 0644); err != nil {
				t.Fatal(err)
			}
			err := GenerateClient(dir, "python", "tasks.yaml", "tasks.py")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q error, got %v", tc.want, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "tasks.py")); !os.IsNotExist(err) {
				t.Fatal("invalid input must not produce a partial client")
			}
		})
	}
}

func TestClientGeneratorSharedExampleSchemas(t *testing.T) {
	for _, language := range []string{"python", "nodejs"} {
		out := "tasks.py"
		if language == "nodejs" {
			out = "tasks.js"
		}
		if err := GenerateClient("../../..", language, "examples/simple/api/tasks/tasks.yaml", filepath.Join(t.TempDir(), out)); err != nil {
			t.Fatal(err)
		}
	}
}
