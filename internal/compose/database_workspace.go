package compose

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// QueryPostgresDatabase executes SQL, not psql commands, as the database owner.
// Local socket authentication stays inside the managed container. No shell is
// involved and SQL travels as one -c argument (never as a psql input script).
func (r CommandRunner) QueryPostgresDatabase(ctx context.Context, projectDir, serviceName, database, owner, sql string) (string, error) {
	if serviceName == "" || database == "" || owner == "" || strings.TrimSpace(sql) == "" || strings.HasPrefix(strings.TrimSpace(sql), `\`) || strings.ContainsRune(sql, 0) {
		return "", errors.New("invalid database query arguments")
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	composeFile, err := findComposeFile(projectDir)
	if err != nil {
		return "", fmt.Errorf("find Compose file: %w", err)
	}
	if err := r.verifyProjectOwnership(ctx, projectDir); err != nil {
		return "", err
	}
	command := composeCommand(ctx, binary, projectDir, composeFile,
		"exec", "-T", "-e", "PGOPTIONS=-c statement_timeout=30000 -c lock_timeout=5000", serviceName,
		"psql", "-X", "--csv", "--quiet", "--pset", "footer=off", "--pset", "null=NULL",
		"--set", "ON_ERROR_STOP=1", "--username="+owner, "--dbname="+database, "-c", sql)
	output, err := runStructuredCommand(command, "run database query", projectDir)
	if err != nil {
		// PostgreSQL diagnostics can echo SQL literals, including secrets.
		return "", errors.New("SQL execution failed or exceeded its time/output limit; check the statement and database availability")
	}
	return string(output), nil
}
