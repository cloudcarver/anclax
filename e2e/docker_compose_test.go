package e2e_test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestComposeDatabasePublicationIsLoopbackOnly(t *testing.T) {
	raw, err := os.ReadFile("docker-compose.yaml")
	if err != nil {
		t.Fatalf("read Compose file: %v", err)
	}

	var compose struct {
		Services map[string]struct {
			Ports       []string          `yaml:"ports"`
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &compose); err != nil {
		t.Fatalf("parse Compose file: %v", err)
	}

	database, ok := compose.Services["db"]
	if !ok {
		t.Fatal("Compose file has no database service")
	}
	if len(database.Ports) != 1 || database.Ports[0] != "127.0.0.1:7432:7432" {
		t.Fatalf("database port must be published only on loopback, got %v", database.Ports)
	}

	password := database.Environment["POSTGRES_PASSWORD"]
	if !strings.Contains(password, "${ANCLAX_E2E_POSTGRES_PASSWORD:?") {
		t.Fatalf("database password must be injected by the test runner, got %q", password)
	}
}
