package compose

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// MySQLFlavor selects the client tools of a MySQL-compatible official image.
// MariaDB 11 no longer ships the mysql-named binaries.
type MySQLFlavor string

const (
	MySQLFlavorMariaDB MySQLFlavor = "mariadb"
	MySQLFlavorMySQL   MySQLFlavor = "mysql"
)

// mysqlTools returns the client, dump tool, and root password variable that
// the official image of a flavor provides. The values are fixed script text:
// nothing user-controlled is ever interpolated into the container shell.
func (f MySQLFlavor) tools() (client, dump, passwordVariable string, err error) {
	switch f {
	case MySQLFlavorMariaDB:
		return "mariadb", "mariadb-dump", "MARIADB_ROOT_PASSWORD", nil
	case MySQLFlavorMySQL:
		return "mysql", "mysqldump", "MYSQL_ROOT_PASSWORD", nil
	default:
		return "", "", "", errors.New("unsupported MySQL flavor")
	}
}

// mysqlConnection authenticates as root over loopback TCP. Loopback is used
// instead of the socket because the image's temporary initialization server
// only listens on the socket; TCP therefore succeeds only once the real server
// is ready. The password comes from the container's own environment, so it
// never passes through the host command line.
func mysqlConnection(tool, passwordVariable string) string {
	return `MYSQL_PWD="$` + passwordVariable + `" exec ` + tool + ` --protocol=tcp --host=127.0.0.1 --user=root --default-character-set=utf8mb4`
}

// ExecMySQLSQL runs SQL through the root client inside a MariaDB or MySQL
// service container and returns its trimmed tab-separated output. The
// statements travel on stdin rather than the command line, so generated
// passwords stay out of the host process list.
func (r CommandRunner) ExecMySQLSQL(ctx context.Context, projectDir, serviceName string, flavor MySQLFlavor, sql string) (string, error) {
	client, _, passwordVariable, err := flavor.tools()
	if err != nil {
		return "", err
	}
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return "", errors.New("Compose service name is required")
	}
	if strings.TrimSpace(sql) == "" {
		return "", errors.New("SQL statement is required")
	}
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
	script := mysqlConnection(client, passwordVariable) + ` --batch --skip-column-names`
	command := composeCommand(ctx, binary, projectDir, composeFile, "exec", "-T", serviceName, "sh", "-c", script)
	command.Stdin = strings.NewReader(sql)
	output, err := runStructuredCommand(command, "run MySQL statement", projectDir)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// BackupMySQL writes a plain SQL dump of one database produced inside a
// MariaDB or MySQL service container. The database name is a single exec
// argument ("$0") after "--", so it is never parsed by the shell or as an option.
func (r CommandRunner) BackupMySQL(ctx context.Context, projectDir, serviceName string, flavor MySQLFlavor, database, destination string) error {
	_, dump, passwordVariable, err := flavor.tools()
	if err != nil {
		return err
	}
	if strings.TrimSpace(database) == "" {
		return errors.New("database name is required")
	}
	output, err := openManagedOutput(destination)
	if err != nil {
		return fmt.Errorf("open MySQL backup: %w", err)
	}
	defer output.Close()

	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	composeFile, err := findComposeFile(projectDir)
	if err != nil {
		return fmt.Errorf("find Compose file: %w", err)
	}
	if err := r.verifyProjectOwnership(ctx, projectDir); err != nil {
		return err
	}
	script := mysqlConnection(dump, passwordVariable) + ` --single-transaction --routines --events --triggers --databases -- "$0"`
	command := composeCommand(ctx, binary, projectDir, composeFile, "exec", "-T", serviceName, "sh", "-c", script, database)
	command.Stdout = output
	stderr := newTailBuffer(maxCommandOutput)
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return composeCommandErrorForProject("create MySQL backup", err, stderr.Bytes(), projectDir)
	}
	return nil
}

// RestoreMySQL replays a plain SQL dump through the root client inside a
// MariaDB or MySQL service container. The client stops at the first error.
func (r CommandRunner) RestoreMySQL(ctx context.Context, projectDir, serviceName string, flavor MySQLFlavor, source string) error {
	client, _, passwordVariable, err := flavor.tools()
	if err != nil {
		return err
	}
	input, err := openManagedInput(source)
	if err != nil {
		return fmt.Errorf("open MySQL backup: %w", err)
	}
	defer input.Close()

	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	composeFile, err := findComposeFile(projectDir)
	if err != nil {
		return fmt.Errorf("find Compose file: %w", err)
	}
	if err := r.verifyProjectOwnership(ctx, projectDir); err != nil {
		return err
	}
	command := composeCommand(ctx, binary, projectDir, composeFile, "exec", "-T", serviceName, "sh", "-c", mysqlConnection(client, passwordVariable))
	command.Stdin = input
	stderr := newTailBuffer(maxCommandOutput)
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return composeCommandErrorForProject("restore MySQL backup", err, stderr.Bytes(), projectDir)
	}
	return nil
}
