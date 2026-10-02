package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudcarver/anclax/pkg/config"
	"gopkg.in/yaml.v3"
)

func TestGeneratedProjectRequiresExplicitAccountCreation(t *testing.T) {
	projectDir := generateProjectForSecurityTest(t)

	appSource := readGeneratedFile(t, projectDir, "app", "app.go")
	if strings.Contains(appSource, "CreateNewUser(") {
		t.Fatal("generated application startup must not create a preset user")
	}
	var development struct {
		Anclax config.Config `yaml:"anclax"`
	}
	if err := yaml.Unmarshal([]byte(readGeneratedFile(t, projectDir, "dev", "app.yaml")), &development); err != nil {
		t.Fatal(err)
	}
	if !development.Anclax.EnableSimpleAuth || development.Anclax.TestAccount != nil {
		t.Fatal("development config must allow registration without a preset account")
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
	if _, err := os.Stat(filepath.Join(projectDir, ".env")); !os.IsNotExist(err) {
		t.Fatalf("local credentials were embedded in the scaffold: %v", err)
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
	applicationPassword, ok := application.Environment["MYAPP_ANCLAX_PG_PASSWORD"].(string)
	if !ok || applicationPassword != password {
		t.Fatal("generated application must use the configured database password")
	}
	if _, ok := application.Environment["MYAPP_ANCLAX_PG_DSN"]; ok {
		t.Fatal("generated application must pass passwords separately instead of interpolating a URI")
	}
	if application.Environment["MYAPP_ANCLAX_PG_HOST"] != "db" || application.Environment["MYAPP_ANCLAX_PG_SSLMODE"] != "disable" {
		t.Fatal("generated application must connect to its development database")
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
