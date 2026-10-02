package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestInitializedComposeExposesApplication(t *testing.T) {
	dir := t.TempDir()
	if err := initFiles(dir, "example.com/scaffold"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "docker-compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(content, &compose); err != nil {
		t.Fatal(err)
	}
	env := compose.Services["dev"].Environment
	if env["MYAPP_ANCLAX_HOST"] != "0.0.0.0" || env["MYAPP_ANCLAX_PORT"] != "2910" {
		t.Fatalf("scaffold listener does not match its published port: %v", env)
	}
}

func TestInitializedProject(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and tests the scaffold as a separate Go module")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := initFiles(dir, "example.com/scaffold"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "anchor.yaml")); !os.IsNotExist(err) {
		t.Fatalf("obsolete config survived in scaffold: %v", err)
	}
	clientPath := filepath.Join(dir, "pkg/apiclient/client.go")
	clientSource, err := os.ReadFile(clientPath)
	if err != nil {
		t.Fatal(err)
	}
	customClient := bytes.Replace(clientSource, []byte("30 * time.Second"), []byte("45 * time.Second"), 1)
	customClient = bytes.Replace(customClient, []byte("10 << 20"), []byte("64 << 20"), 1)
	if bytes.Equal(customClient, clientSource) {
		t.Fatal("client defaults were not customized")
	}
	if err := os.WriteFile(clientPath, customClient, 0644); err != nil {
		t.Fatal(err)
	}
	config, err := parseConfig(filepath.Join(dir, "anclax.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := genSchemas(dir, config.Schemas); err != nil {
		t.Fatal(err)
	}
	for i := range config.OapiCodegen {
		if err := genOapi(dir, &config.OapiCodegen[i], config.Schemas); err != nil {
			t.Fatal(err)
		}
	}
	regeneratedClient, err := os.ReadFile(clientPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(regeneratedClient, customClient) {
		t.Fatal("API generation overwrote application client policy")
	}
	for _, args := range [][]string{
		{"mod", "edit", "-replace=github.com/cloudcarver/anclax=" + root},
		{"test", "-mod=mod", "./..."},
	} {
		cmd := exec.CommandContext(t.Context(), "go", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, out)
		}
	}
}
