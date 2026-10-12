package service

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"redlaunch/internal/application"
)

type databaseQueryRunner interface {
	QueryPostgresDatabase(context.Context, string, string, string, string, string) (string, error)
}

// GetDatabaseWorkspace returns a managed database and its user schemas/tables.
func (s *ManagedDatabases) GetDatabaseWorkspace(ctx context.Context, name string) (application.ManagedDatabase, []application.DatabaseSchema, error) {
	item, directory, runner, err := s.databaseQueryTarget(ctx, name)
	if err != nil {
		return item, nil, err
	}
	output, err := runner.QueryPostgresDatabase(ctx, directory, managedDatabaseServiceName, item.Name, item.Owner, `SELECT n.nspname, COALESCE(c.relname, '')
FROM pg_catalog.pg_namespace n
LEFT JOIN pg_catalog.pg_class c ON c.relnamespace = n.oid AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
WHERE left(n.nspname, 3) <> 'pg_' AND n.nspname <> 'information_schema'
ORDER BY n.nspname, c.relname;`)
	if err != nil {
		return item, nil, err
	}
	records, err := csv.NewReader(strings.NewReader(output)).ReadAll()
	if err != nil || len(records) == 0 || len(records[0]) != 2 {
		return item, nil, errors.New("database schemas could not be read")
	}
	var schemas []application.DatabaseSchema
	for _, row := range records[1:] {
		if len(schemas) == 0 || schemas[len(schemas)-1].Name != row[0] {
			schemas = append(schemas, application.DatabaseSchema{Name: row[0]})
		}
		if row[1] != "" {
			schema := &schemas[len(schemas)-1]
			schema.Tables = append(schema.Tables, application.DatabaseTable{Name: row[1], SQL: databaseTableSQL(row[0], row[1])})
		}
	}
	return item, schemas, nil
}

func databaseTableSQL(schema, table string) string {
	return "SELECT * FROM " + postgresQuoteIdentifier(schema) + "." + postgresQuoteIdentifier(table) + " LIMIT 1000;"
}

// RunDatabaseQuery explicitly executes SQL using the database owner's role.
func (s *ManagedDatabases) RunDatabaseQuery(ctx context.Context, name, sql string) (application.DatabaseQueryResult, error) {
	var result application.DatabaseQueryResult
	if len(sql) > 64*1024 || strings.TrimSpace(sql) == "" || strings.HasPrefix(strings.TrimSpace(sql), `\`) || strings.ContainsRune(sql, 0) {
		return result, errors.New("enter SQL (up to 64 KiB); psql commands are not supported")
	}
	item, directory, runner, err := s.databaseQueryTarget(ctx, name)
	if err != nil {
		return result, err
	}
	started := time.Now()
	output, err := runner.QueryPostgresDatabase(ctx, directory, managedDatabaseServiceName, item.Name, item.Owner, sql)
	result.Duration = time.Since(started)
	if err != nil {
		return result, errors.New("SQL execution failed or exceeded its time/output limit; check the statement and database availability")
	}
	records, err := csv.NewReader(strings.NewReader(output)).ReadAll()
	if err != nil {
		// Scripts with multiple result sets can have different column counts.
		result.Output = output
		return result, nil
	}
	if len(records) > 0 {
		result.Columns = records[0]
		result.Rows = records[1:]
		if len(result.Rows) > 1000 {
			result.Rows = result.Rows[:1000]
			result.Truncated = true
		}
	}
	return result, nil
}

// ImportDatabaseDump restores uploaded bytes without using the upload's filename
// or retaining its contents on disk. SQL diagnostics must not expose dump data.
func (s *ManagedDatabases) ImportDatabaseDump(ctx context.Context, name string, source io.Reader) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	contents, err := io.ReadAll(io.LimitReader(source, application.DatabaseDumpMaxBytes+1))
	if err != nil || len(contents) == 0 || len(contents) > application.DatabaseDumpMaxBytes {
		return errors.New("select a non-empty SQL or custom-format dump up to 32 MiB")
	}
	item, directory, _, err := s.databaseQueryTarget(ctx, name)
	if err != nil {
		return err
	}
	runner, ok := s.runner.(interface {
		ImportPostgresDatabase(context.Context, string, string, string, string, []byte) error
	})
	if !ok {
		return errors.New("database dump import is not configured")
	}
	lease, err := s.projectLocks.acquire(ctx, managedDatabaseProjectLockKey)
	if err != nil {
		return err
	}
	defer lease.release()
	if err := runner.ImportPostgresDatabase(ctx, directory, managedDatabaseServiceName, item.Name, item.Owner, contents); err != nil {
		return errors.New("dump import failed; check the format, owner permissions, conflicting objects, and PostgreSQL version")
	}
	return nil
}

func (s *ManagedDatabases) databaseQueryTarget(ctx context.Context, name string) (application.ManagedDatabase, string, databaseQueryRunner, error) {
	var item application.ManagedDatabase
	name, err := application.ValidateManagedDatabaseName(name)
	if err != nil {
		return item, "", nil, err
	}
	if err := s.requireEnabled(ctx); err != nil {
		return item, "", nil, err
	}
	item, err = s.repository.GetManagedDatabase(ctx, name)
	if err != nil {
		return item, "", nil, err
	}
	directory, err := s.managedDirectory()
	if err != nil {
		return item, "", nil, err
	}
	runner, ok := s.runner.(databaseQueryRunner)
	if !ok {
		return item, "", nil, errors.New("database queries are not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Never offer arbitrary SQL under an administrative or host-access role,
	// including inherited/SET ROLE memberships. This query is trusted SQL.
	statement := fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE
pg_has_role(%s, oid, 'MEMBER') AND (rolsuper OR rolcreaterole OR rolcreatedb OR rolreplication OR rolbypassrls
OR rolname IN ('pg_read_server_files', 'pg_write_server_files', 'pg_execute_server_program')));`, postgresQuoteLiteral(item.Owner))
	privileged, err := s.execSQL(ctx, directory, statement)
	if err != nil || strings.TrimSpace(privileged) != "f" {
		return item, "", nil, errors.New("SQL workspace requires a non-administrative database owner without host-access privileges")
	}
	return item, directory, runner, nil
}
