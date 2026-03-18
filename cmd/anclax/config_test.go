package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseConfigSupportsSingleGeneratorConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "anclax.yaml")
	configYAML := `oapi-codegen:
  path: api/v1.yaml
  out: pkg/zgen/apigen/spec_gen.go
  package: apigen
wire:
  path: ./wire
task-handler:
  path: api/tasks.yaml
  package: taskgen
  out: pkg/zgen/taskgen/runner_gen.go
sqlc:
  path: dev/sqlc.yaml
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	config, err := parseConfig(configPath)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}

	if got := len(config.OapiCodegen); got != 1 {
		t.Fatalf("oapi-codegen len = %d, want 1", got)
	}
	if got := config.OapiCodegen[0].Path; got != "api/v1.yaml" {
		t.Fatalf("oapi-codegen[0].path = %q, want %q", got, "api/v1.yaml")
	}

	if got := len(config.Wire); got != 1 {
		t.Fatalf("wire len = %d, want 1", got)
	}
	if got := config.Wire[0].Path; got != "./wire" {
		t.Fatalf("wire[0].path = %q, want %q", got, "./wire")
	}

	if got := len(config.TaskHandler); got != 1 {
		t.Fatalf("task-handler len = %d, want 1", got)
	}
	if got := config.TaskHandler[0].Path; got != "api/tasks.yaml" {
		t.Fatalf("task-handler[0].path = %q, want %q", got, "api/tasks.yaml")
	}

	if got := len(config.Sqlc); got != 1 {
		t.Fatalf("sqlc len = %d, want 1", got)
	}
	if got := config.Sqlc[0].Path; got != "dev/sqlc.yaml" {
		t.Fatalf("sqlc[0].path = %q, want %q", got, "dev/sqlc.yaml")
	}
}

func TestParseConfigSupportsGeneratorConfigArrays(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "anclax.yaml")
	configYAML := `oapi-codegen:
  - path: api/v1.yaml
    out: pkg/zgen/apigen/spec_gen.go
    package: apigen
  - path: api/admin.yaml
    out: pkg/zgen/admin/spec_gen.go
    package: admingen
wire:
  - path: ./wire
  - path: ./internal/wire
task-handler:
  - path: api/tasks.yaml
    package: taskgen
    out: pkg/zgen/taskgen/runner_gen.go
  - path: api/tasks-admin.yaml
    package: admintaskgen
    out: pkg/zgen/taskgen/admin_runner_gen.go
sqlc:
  - path: dev/sqlc.yaml
  - path: dev/sqlc-admin.yaml
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	config, err := parseConfig(configPath)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}

	if got := len(config.OapiCodegen); got != 2 {
		t.Fatalf("oapi-codegen len = %d, want 2", got)
	}
	if got := config.OapiCodegen[1].Package; got != "admingen" {
		t.Fatalf("oapi-codegen[1].package = %q, want %q", got, "admingen")
	}

	if got := len(config.Wire); got != 2 {
		t.Fatalf("wire len = %d, want 2", got)
	}
	if got := config.Wire[1].Path; got != "./internal/wire" {
		t.Fatalf("wire[1].path = %q, want %q", got, "./internal/wire")
	}

	if got := len(config.TaskHandler); got != 2 {
		t.Fatalf("task-handler len = %d, want 2", got)
	}
	if got := config.TaskHandler[1].Package; got != "admintaskgen" {
		t.Fatalf("task-handler[1].package = %q, want %q", got, "admintaskgen")
	}

	if got := len(config.Sqlc); got != 2 {
		t.Fatalf("sqlc len = %d, want 2", got)
	}
	if got := config.Sqlc[1].Path; got != "dev/sqlc-admin.yaml" {
		t.Fatalf("sqlc[1].path = %q, want %q", got, "dev/sqlc-admin.yaml")
	}
}
