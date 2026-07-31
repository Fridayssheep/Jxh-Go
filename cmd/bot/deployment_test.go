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
	normalizedCompose := strings.ReplaceAll(compose, "\r\n", "\n")
	makefile := readDeploymentFile(t, filepath.Join(repositoryRoot, "Makefile"))
	initializer := readDeploymentFile(t, filepath.Join(repositoryRoot, "deploy", "mysql", "init", "001_schema.sql"))
	entrypoint := readDeploymentFile(t, filepath.Join(repositoryRoot, "scripts", "entrypoint.sh"))
	dockerignore := readDeploymentFile(t, filepath.Join(repositoryRoot, ".dockerignore"))
	gitignore := readDeploymentFile(t, filepath.Join(repositoryRoot, ".gitignore"))

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

	if count := strings.Count(compose, "- ./data/config:/app/config"); count != 1 {
		t.Errorf("configuration directory mounts = %d, want 1 for bot", count)
	}
	for _, required := range []string{
		"JXH_BOT_RESTART_MODE: supervised_exit",
		"restart: unless-stopped",
		"mysql:\n        condition: service_healthy",
	} {
		if !strings.Contains(normalizedCompose, required) {
			t.Errorf("docker-compose.yaml does not contain %q", required)
		}
	}
	if strings.Contains(compose, ":/app/config/config.yaml") {
		t.Error("docker-compose.yaml uses a single-file configuration bind mount")
	}
	for name, contents := range map[string]string{
		"Dockerfile":          dockerfile,
		"docker-compose.yaml": compose,
		"Makefile":            makefile,
		"initializer":         initializer,
		".dockerignore":       dockerignore,
		".gitignore":          gitignore,
	} {
		for _, forbidden := range []string{
			"jxh-migrate", "cmd/migrate", "deploy/mysql/migrations", "schema_migrations", "schema_migration_attempts",
		} {
			if strings.Contains(contents, forbidden) {
				t.Errorf("%s still contains %q", name, forbidden)
			}
		}
	}
	for _, removed := range []string{
		filepath.Join(repositoryRoot, "cmd", "migrate"),
		filepath.Join(repositoryRoot, "deploy", "mysql", "migrations"),
	} {
		entries, err := os.ReadDir(removed)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("removed migration path still contains files: %s", removed)
		}
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
