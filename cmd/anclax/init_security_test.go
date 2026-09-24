package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGeneratedProjectRequiresExplicitAccountCreation(t *testing.T) {
	projectDir := generateProjectForSecurityTest(t)

	appSource := readGeneratedFile(t, projectDir, "app", "app.go")
	if strings.Contains(appSource, "CreateNewUser(") {
		t.Fatal("generated application startup must not create a preset user")
	}

	readme := readGeneratedFile(t, projectDir, "README.md")
	if !strings.Contains(readme, "/auth/sign-up") {
		t.Fatal("generated README must document explicit first-user sign-up")
	}
	if strings.Contains(readme, `"password": "test"`) || strings.Contains(readme, "test/test") {
		t.Fatal("generated README must not publish preset account credentials")
	}

	gitignore := readGeneratedFile(t, projectDir, ".gitignore")
	if !containsLine(gitignore, ".env") {
		t.Fatal("generated project must ignore the local environment file")
	}
}

func TestGeneratedComposeKeepsDatabasePrivateAndRequiresPassword(t *testing.T) {
	projectDir := generateProjectForSecurityTest(t)
	raw := readGeneratedFile(t, projectDir, "docker-compose.yaml")

	var compose struct {
		Services map[string]struct {
			Ports       []string       `yaml:"ports"`
			Environment map[string]any `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(raw), &compose); err != nil {
		t.Fatalf("parse generated Compose file: %v", err)
	}

	database, ok := compose.Services["db"]
	if !ok {
		t.Fatal("generated Compose file has no database service")
	}
	if len(database.Ports) != 0 {
		t.Fatalf("generated database must not publish host ports, got %v", database.Ports)
	}

	password, ok := database.Environment["POSTGRES_PASSWORD"].(string)
	if !ok || !strings.Contains(password, "${POSTGRES_PASSWORD:?") {
		t.Fatalf("generated database password must come from required configuration, got %v", database.Environment["POSTGRES_PASSWORD"])
	}

	application, ok := compose.Services["dev"]
	if !ok {
		t.Fatal("generated Compose file has no application service")
	}
	dsn, ok := application.Environment["MYAPP_ANCLAX_PG_DSN"].(string)
	if !ok || !strings.Contains(dsn, "${POSTGRES_PASSWORD:?") {
		t.Fatalf("generated application DSN must use the configured database password, got %v", application.Environment["MYAPP_ANCLAX_PG_DSN"])
	}
	if strings.Contains(dsn, "postgres:postgres@") {
		t.Fatal("generated application DSN must not contain the old fixed password")
	}
}

func generateProjectForSecurityTest(t *testing.T) string {
	t.Helper()

	projectDir := t.TempDir()
	if err := initFiles(projectDir, "example.com/generated"); err != nil {
		t.Fatalf("generate project: %v", err)
	}
	return projectDir
}

func readGeneratedFile(t *testing.T, projectDir string, path ...string) string {
	t.Helper()

	parts := append([]string{projectDir}, path...)
	raw, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatalf("read generated file %q: %v", filepath.Join(path...), err)
	}
	return string(raw)
}

func containsLine(content, want string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}
