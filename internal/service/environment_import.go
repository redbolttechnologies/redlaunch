package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"redlaunch/internal/application"
)

// ImportEnvironmentFiles merges the selected managed environment files for
// an application. Provided entries overwrite matching keys, new keys are
// appended, and existing keys absent from the import are preserved. Missing
// files are created to keep every managed project provisioned with both
// vars.env and secrets.env.
func (s *Applications) ImportEnvironmentFiles(ctx context.Context, applicationID int64, input application.EnvironmentFileImportInput) error {
	if !input.VariablesProvided && !input.SecretsProvided {
		return application.ErrEnvironmentImportFileRequired
	}
	if input.VariablesProvided {
		if err := validateEnvironmentFileContents(input.Variables); err != nil {
			return err
		}
	}
	if input.SecretsProvided {
		if err := validateEnvironmentFileContents(input.Secrets); err != nil {
			return err
		}
	}
	if s.detailsRepository == nil {
		return errors.New("application details repository is not configured")
	}
	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return err
	}
	defer lease.release()

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return err
	}
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return err
	}

	varsPath := filepath.Join(directory, varsEnvFile)
	varsSnapshot, err := snapshotManagedFile(varsPath)
	if err != nil {
		return fmt.Errorf("read application variables file: %w", err)
	}
	secretsPath := filepath.Join(directory, secretsEnvFile)
	secretsSnapshot, err := snapshotManagedFile(secretsPath)
	if err != nil {
		return fmt.Errorf("read application secrets file: %w", err)
	}
	rollbackFiles := func() error {
		return errors.Join(restoreManagedFile(varsSnapshot), restoreManagedFile(secretsSnapshot))
	}

	mergeFile := func(path, label string, snapshot managedFileSnapshot, contents []byte, provided bool) error {
		if !provided {
			if snapshot.exists {
				return nil
			}
			if err := writeManagedFile(path, "", envFileMode); err != nil {
				return fmt.Errorf("write application %s file: %w", label, err)
			}
			return nil
		}
		existing := ""
		if snapshot.exists {
			existing = string(snapshot.contents)
		}
		merged, err := mergeEnvironmentContents(existing, contents)
		if err != nil {
			return err
		}
		if snapshot.exists && merged == existing {
			return nil
		}
		mode := snapshot.mode
		if !snapshot.exists {
			mode = envFileMode
		}
		if err := writeManagedFile(path, merged, mode); err != nil {
			return fmt.Errorf("write application %s file: %w", label, err)
		}
		return nil
	}

	if err := mergeFile(varsPath, "variables", varsSnapshot, input.Variables, input.VariablesProvided); err != nil {
		return errors.Join(err, rollbackFiles())
	}
	if err := mergeFile(secretsPath, "secrets", secretsSnapshot, input.Secrets, input.SecretsProvided); err != nil {
		return errors.Join(err, rollbackFiles())
	}
	return nil
}

// mergeEnvironmentContents merges imported dotenv entries into existing
// managed file contents. Existing entries absent from the import are
// preserved byte-for-byte, matching entries are updated in place (preserving
// the existing line's whitespace, export prefix, and trailing comment), and
// new entries are appended in import order using canonical value encoding
// while preserving the imported trailing comment. Import comments and blank
// lines carry no keys and are not copied.
func mergeEnvironmentContents(existing string, imported []byte) (string, error) {
	importedEntries := parseEnvironmentEntries(string(imported))
	if len(importedEntries) == 0 {
		return existing, nil
	}
	if existing == "" {
		lines := make([]string, 0, len(importedEntries))
		for _, entry := range importedEntries {
			lines = append(lines, entry.key+"="+formatEnvironmentValue(entry.value)+importedEntryCommentSuffix(entry.rawValueWithComment))
		}
		merged := strings.Join(lines, "\n")
		if len(lines) > 0 {
			merged += "\n"
		}
		if len(merged) > application.MaxEnvironmentFileSize {
			return "", application.ErrEnvironmentImportFileTooLarge
		}
		return merged, nil
	}

	lineEnding := "\n"
	contents := existing
	if strings.Contains(contents, "\r\n") {
		lineEnding = "\r\n"
		contents = strings.ReplaceAll(contents, "\r\n", "\n")
	}
	bom := ""
	if strings.HasPrefix(contents, "\ufeff") {
		bom = "\ufeff"
		contents = strings.TrimPrefix(contents, "\ufeff")
	}
	trailingNewline := strings.HasSuffix(contents, "\n")
	if trailingNewline {
		contents = strings.TrimSuffix(contents, "\n")
	}
	lines := strings.Split(contents, "\n")

	indexByKey := make(map[string]int, len(importedEntries))
	valueByKey := make(map[string]string, len(importedEntries))
	countByKey := make(map[string]int)
	for index, line := range lines {
		key, value, ok := parseEnvironmentEntry(line)
		if !ok {
			continue
		}
		countByKey[key]++
		if _, seen := indexByKey[key]; !seen {
			indexByKey[key] = index
			valueByKey[key] = value
		}
	}

	appended := false
	for _, entry := range importedEntries {
		if index, ok := indexByKey[entry.key]; ok {
			if countByKey[entry.key] > 1 {
				return "", application.ErrEnvironmentVariableDuplicate
			}
			if valueByKey[entry.key] != entry.value {
				lines[index] = formatEnvironmentEntry(lines[index], entry.key, entry.value)
			}
			continue
		}
		lines = append(lines, entry.key+"="+formatEnvironmentValue(entry.value)+importedEntryCommentSuffix(entry.rawValueWithComment))
		indexByKey[entry.key] = len(lines) - 1
		valueByKey[entry.key] = entry.value
		countByKey[entry.key] = 1
		appended = true
	}

	updated := strings.Join(lines, "\n")
	if trailingNewline || appended {
		updated += "\n"
	}
	if lineEnding != "\n" {
		updated = strings.ReplaceAll(updated, "\n", lineEnding)
	}
	merged := bom + updated
	if len(merged) > application.MaxEnvironmentFileSize {
		return "", application.ErrEnvironmentImportFileTooLarge
	}
	return merged, nil
}

// importedEntryCommentSuffix returns the trailing comment of an imported
// entry, ensuring it remains separated from the value. Trimming during parsing
// can drop the separating space for empty values (for example,
// `EMPTY= # comment` parses to `# comment`), which would otherwise change the
// meaning to a literal value.
func importedEntryCommentSuffix(rawValueWithComment string) string {
	comment := environmentCommentSuffix(rawValueWithComment)
	if comment == "" {
		return ""
	}
	if comment[0] != ' ' && comment[0] != '\t' {
		return " " + comment
	}
	return comment
}
