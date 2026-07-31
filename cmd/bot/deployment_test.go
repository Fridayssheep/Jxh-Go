package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentPersistsConfigurationDirectory(t *testing.T) {
	repositoryRoot := filepath.Join("..", "..")
	dockerfile := readDeploymentFile(t, filepath.Join(repositoryRoot, "Dockerfile"))
	compose := readDeploymentFile(t, filepath.Join(repositoryRoot, "docker-compose.yaml"))
	entrypoint := readDeploymentFile(t, filepath.Join(repositoryRoot, "scripts", "entrypoint.sh"))

	for _, required := range []string{
		"COPY config.example.yaml /usr/local/share/jxh/config.example.yaml",
		`CMD ["jxh-bot", "-config", "/app/config/config.yaml"]`,
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("Dockerfile does not contain %q", required)
		}
	}
	if strings.Contains(dockerfile, "/app/config.yaml") {
		t.Error("Dockerfile still uses the non-persistent single-file configuration path")
	}

	if count := strings.Count(compose, "- ./data/config:/app/config"); count != 2 {
		t.Errorf("configuration directory mounts = %d, want 2 for migrate and bot", count)
	}
	for _, required := range []string{
		`command: ["jxh-migrate", "-config", "/app/config/config.yaml", "-dir", "/app/migrations"]`,
		"JXH_BOT_RESTART_MODE: supervised_exit",
		"restart: unless-stopped",
	} {
		if !strings.Contains(compose, required) {
			t.Errorf("docker-compose.yaml does not contain %q", required)
		}
	}
	if strings.Contains(compose, ":/app/config/config.yaml") {
		t.Error("docker-compose.yaml uses a single-file configuration bind mount")
	}

	for _, required := range []string{
		`if [ ! -f "/app/config/config.yaml" ]; then`,
		`cp /usr/local/share/jxh/config.example.yaml /app/config/config.yaml`,
		`chmod 0600 /app/config/config.yaml`,
	} {
		if !strings.Contains(entrypoint, required) {
			t.Errorf("entrypoint.sh does not contain %q", required)
		}
	}
}

func readDeploymentFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
