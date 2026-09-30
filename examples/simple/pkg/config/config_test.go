package config

import (
	"os"
	"testing"
)

func TestDevelopmentConfig(t *testing.T) {
	data, err := os.ReadFile("../../dev/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if err := os.WriteFile("app.yaml", data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MYAPP_ANCLAX_PORT", "2910")
	t.Setenv("MYAPP_ANCLAX_PG_DSN", "postgres://postgres:postgres@db:5432/postgres?sslmode=disable")
	cfg, err := NewConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Anclax.EnableSimpleAuth || cfg.Anclax.TestAccount == nil || cfg.Anclax.TestAccount.Password != "test" {
		t.Fatal("development auth and test account are not configured")
	}
	if cfg.Anclax.Port != 2910 || cfg.Anclax.Pg.DSN == nil || *cfg.Anclax.Pg.DSN != os.Getenv("MYAPP_ANCLAX_PG_DSN") {
		t.Fatal("Compose environment overrides were not loaded")
	}
}
