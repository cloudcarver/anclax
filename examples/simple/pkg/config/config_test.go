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
	t.Setenv("MYAPP_ANCLAX_PG_HOST", "db")
	t.Setenv("MYAPP_ANCLAX_PG_PORT", "5432")
	t.Setenv("MYAPP_ANCLAX_PG_USER", "postgres")
	t.Setenv("MYAPP_ANCLAX_PG_PASSWORD", "custom:@/?#$%password")
	t.Setenv("MYAPP_ANCLAX_PG_DB", "postgres")
	t.Setenv("MYAPP_ANCLAX_PG_SSLMODE", "disable")
	cfg, err := NewConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Anclax.EnableSimpleAuth || cfg.Anclax.TestAccount != nil {
		t.Fatal("development config must enable registration without creating a preset account")
	}
	pg := cfg.Anclax.Pg
	if cfg.Anclax.Port != 2910 || pg.DSN != nil || pg.Host != "db" || pg.Port != 5432 || pg.User != "postgres" || pg.Db != "postgres" || pg.SSLMode != "disable" || pg.Password != os.Getenv("MYAPP_ANCLAX_PG_PASSWORD") {
		t.Fatal("Compose environment overrides were not loaded")
	}
}
