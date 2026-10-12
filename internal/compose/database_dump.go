package compose

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const maxDatabaseDumpBytes = 32 * 1024 * 1024

// ImportPostgresDatabase restores into the selected database as its owner.
// SQL streams run under psql restricted mode: no shell, file, connection, or
// variable meta-commands are allowed. Custom archives use pg_restore directly.
func (r CommandRunner) ImportPostgresDatabase(ctx context.Context, projectDir, serviceName, database, owner string, contents []byte) error {
	if serviceName == "" || database == "" || owner == "" || len(contents) == 0 || len(contents) > maxDatabaseDumpBytes {
		return errors.New("invalid dump import arguments")
	}
	custom := bytes.HasPrefix(contents, []byte("PGDMP"))
	var key string
	if !custom {
		if !utf8.Valid(contents) || bytes.ContainsRune(contents, 0) {
			return errors.New("dump must be UTF-8 SQL or a PostgreSQL custom archive")
		}
		contents = stripDumpRestrictionEnvelope(contents)
		var random [32]byte
		if _, err := rand.Read(random[:]); err != nil {
			return errors.New("could not secure SQL dump import")
		}
		key = hex.EncodeToString(random[:])
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	composeFile, err := findComposeFile(projectDir)
	if err != nil {
		return errors.New("managed Compose project is unavailable")
	}
	if err := r.verifyProjectOwnership(ctx, projectDir); err != nil {
		return errors.New("managed Compose project ownership could not be verified")
	}
	args := []string{"exec", "-T", "-e", "PGOPTIONS=-c statement_timeout=600000 -c lock_timeout=5000", serviceName}
	if custom {
		args = append(args, "pg_restore", "--no-owner", "--no-privileges", "--exit-on-error", "--single-transaction", "--username="+owner, "--dbname="+database)
	} else {
		// -c runs before -f, and -X avoids container startup scripts. Unknown
		// \restrict on an outdated client fails closed via ON_ERROR_STOP.
		args = append(args, "psql", "-X", "--quiet", "--set", "ON_ERROR_STOP=1", "--single-transaction", "--username="+owner, "--dbname="+database, "-c", `\restrict `+key, "-f", "-")
	}
	command := composeCommand(ctx, binary, projectDir, composeFile, args...)
	command.Stdin = bytes.NewReader(contents)
	command.Stdout = newTailBuffer(maxCommandOutput)
	command.Stderr = newTailBuffer(maxCommandOutput)
	if err := command.Run(); err != nil {
		return errors.New("dump restore failed; no SQL diagnostics are exposed because they may contain secrets")
	}
	return nil
}

var dumpRestrictionLine = regexp.MustCompile(`^\\restrict ([A-Za-z0-9]+)\r?$`)

// Current pg_dump wraps SQL in its own restriction key. Remove ONLY a paired
// outer envelope surrounded by blank/comment lines, never arbitrary directives
// or COPY data. Our fresh, unpredictable key remains active for the entire file.
func stripDumpRestrictionEnvelope(contents []byte) []byte {
	lines := strings.Split(string(contents), "\n")
	first, last := 0, len(lines)-1
	isComment := func(line string) bool {
		trimmed := strings.TrimSpace(line)
		return trimmed == "" || strings.HasPrefix(trimmed, "--")
	}
	for first <= last && isComment(lines[first]) {
		first++
	}
	for last >= first && isComment(lines[last]) {
		last--
	}
	if first >= last {
		return contents
	}
	match := dumpRestrictionLine.FindStringSubmatch(lines[first])
	if len(match) != 2 || strings.TrimSuffix(lines[last], "\r") != `\unrestrict `+match[1] {
		return contents
	}
	lines[first], lines[last] = "", ""
	return []byte(strings.Join(lines, "\n"))
}
