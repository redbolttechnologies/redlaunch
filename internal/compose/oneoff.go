package compose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"redlaunch/internal/application"
)

const (
	// maxOneOffRawOutput bounds how much of each stream is kept while the
	// process runs. It is deliberately larger than the final output limit so
	// secret redaction and truncation happen on a useful prefix.
	maxOneOffRawOutput = 1 << 20
	// maxOneOffOutputPerStream is the retained output limit per stream.
	maxOneOffOutputPerStream = 64 << 10
	// maxOneOffOutputTotal is the retained output limit across both streams.
	maxOneOffOutputTotal = 128 << 10
)

// OneOffRunParameters contains validated per-run overrides. The compose layer
// defines its own type so callers depend on the runner contract rather than
// the HTTP/domain representation.
type OneOffRunParameters struct {
	Args []string
	Env  map[string]string
}

// OneOffRunResult is the bounded, redacted outcome of a one-off Compose run.
type OneOffRunResult struct {
	ExitCode  int
	Stdout    string
	Stderr    string
	Truncated bool
	Duration  time.Duration
}

// RunOneOff executes one Compose service as a one-off task and removes its
// container afterwards (docker compose run --rm). Per-run arguments are
// appended after the service name and environment overrides travel as repeated
// --env flags. The service's configured image, command, and entrypoint remain
// authoritative.
func (r CommandRunner) RunOneOff(ctx context.Context, projectDir, serviceName string, parameters OneOffRunParameters) (OneOffRunResult, error) {
	started := time.Now()
	ctx = normalizeContext(ctx)
	result := OneOffRunResult{ExitCode: -1}

	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return result, fmt.Errorf("run compose one-off service: Compose service name is required")
	}
	if _, err := application.ValidateServiceName(serviceName); err != nil {
		return result, fmt.Errorf("run compose one-off service: %w", err)
	}
	normalized, err := application.ValidateOneOffRunParameters(application.OneOffRunParameters{
		Args: parameters.Args,
		Env:  parameters.Env,
	})
	if err != nil {
		return result, fmt.Errorf("run compose one-off service %q: %w", serviceName, err)
	}
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}

	composeFile, err := findComposeFile(projectDir)
	if err != nil {
		return result, fmt.Errorf("find Compose file: %w", err)
	}
	if err := r.verifyProjectOwnership(ctx, projectDir); err != nil {
		return result, err
	}

	args := []string{"--ansi", "never", "run", "--rm", "--quiet-pull", "-T"}
	keys := make([]string, 0, len(normalized.Env))
	for key := range normalized.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--env", key+"="+normalized.Env[key])
	}
	args = append(args, serviceName)
	args = append(args, normalized.Args...)

	command := composeCommand(ctx, binary, projectDir, composeFile, args...)
	// Give Docker Compose a chance to stop and remove the one-off container on
	// cancellation instead of abruptly killing the CLI process. WaitDelay
	// prevents a stuck CLI from holding the job forever.
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return command.Process.Signal(os.Interrupt)
	}
	command.WaitDelay = 5 * time.Second
	stdout := newBoundedBuffer(maxOneOffRawOutput)
	stderr := newBoundedBuffer(maxOneOffRawOutput)
	command.Stdout = stdout
	command.Stderr = stderr
	runErr := command.Run()
	result.Duration = time.Since(started)
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}

	stdoutText := redactAndSanitizeOneOffOutput(string(stdout.Bytes()), projectDir)
	stderrText := redactAndSanitizeOneOffOutput(string(stderr.Bytes()), projectDir)
	stdoutText, stdoutTruncated := truncateUTF8(stdoutText, maxOneOffOutputPerStream)
	stderrText, stderrTruncated := truncateUTF8(stderrText, maxOneOffOutputPerStream)
	if len(stdoutText)+len(stderrText) > maxOneOffOutputTotal {
		remaining := maxOneOffOutputTotal - len(stdoutText)
		if remaining < 0 {
			remaining = 0
		}
		var totalTruncated bool
		stderrText, totalTruncated = truncateUTF8(stderrText, remaining)
		stderrTruncated = stderrTruncated || totalTruncated
	}
	result.Stdout = stdoutText
	result.Stderr = stderrText
	result.Truncated = stdout.truncated || stderr.truncated || stdoutTruncated || stderrTruncated

	if runErr != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			runErr = errors.Join(runErr, contextErr)
		}
		diagnostic := redactAndSanitizeOneOffOutput(string(stderr.Bytes()), projectDir)
		return result, composeCommandErrorForProject(
			fmt.Sprintf("run compose one-off service %q", serviceName),
			runErr,
			[]byte(diagnostic),
			projectDir,
		)
	}
	return result, nil
}

// sanitizeTerminalOutput removes ANSI CSI/OSC escape sequences, other terminal
// control sequences, and control characters before output is stored or shown.
// It keeps newlines and tabs and replaces invalid UTF-8 with U+FFFD.
func sanitizeTerminalOutput(value string) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	runes := []rune(value)
	output := make([]rune, 0, len(runes))
	for index := 0; index < len(runes); index++ {
		character := runes[index]
		switch {
		case character == 0x1b:
			index = skipTerminalEscape(runes, index)
		case character == 0x9b:
			// Single-byte C1 CSI is not valid UTF-8 text; consume its
			// parameter bytes like an ESC [ sequence.
			index = skipCSISequence(runes, index+1)
		case character == '\r':
			// Normalize CRLF and bare CR so progress overwrites become
			// readable lines instead of sequence artefacts.
			if index+1 < len(runes) && runes[index+1] == '\n' {
				continue
			}
			output = append(output, '\n')
		case character == '\b':
			if len(output) > 0 {
				output = output[:len(output)-1]
			}
		case character == '\n' || character == '\t':
			output = append(output, character)
		case character < 0x20 || character == 0x7f || (character >= 0x80 && character <= 0x9f):
			// Drop remaining C0/C1 controls.
		default:
			output = append(output, character)
		}
	}
	return string(output)
}

// skipTerminalEscape consumes one escape sequence and returns the index of its
// last rune. Unknown or incomplete sequences consume only the bytes that belong
// to the sequence so normal text after ESC is preserved.
func skipTerminalEscape(runes []rune, index int) int {
	if index+1 >= len(runes) {
		return index
	}
	switch runes[index+1] {
	case '[':
		return skipCSISequence(runes, index+2)
	case ']', 'P', 'X', '^', '_':
		// OSC/DCS/SOS/PM/APC terminate with BEL or ST (ESC \).
		for cursor := index + 2; cursor < len(runes); cursor++ {
			if runes[cursor] == 0x07 || runes[cursor] == 0x9c {
				return cursor
			}
			if runes[cursor] == 0x1b && cursor+1 < len(runes) && runes[cursor+1] == '\\' {
				return cursor + 1
			}
		}
		return len(runes) - 1
	case '(', ')', '*', '+', '#', '%':
		// Character-set and related designator sequences consume one more
		// rune after the intermediate byte.
		if index+2 < len(runes) {
			return index + 2
		}
		return len(runes) - 1
	default:
		return index + 1
	}
}

func skipCSISequence(runes []rune, start int) int {
	for cursor := start; cursor < len(runes); cursor++ {
		if runes[cursor] >= 0x40 && runes[cursor] <= 0x7e {
			return cursor
		}
	}
	return len(runes) - 1
}

// redactAndSanitizeOneOffOutput redacts known values in the raw stream first so
// secrets containing terminal characters are still removed, then strips
// terminal sequences and redacts the cleaned text once more for assignment
// patterns that only become visible after control bytes are removed.
func redactAndSanitizeOneOffOutput(raw, projectDir string) string {
	return redactOneOffOutput(sanitizeTerminalOutput(redactOneOffOutput(raw, projectDir)), projectDir)
}

func redactOneOffOutput(output, projectDir string) string {
	secrets := diagnosticSecretValues(projectDir)
	sort.SliceStable(secrets, func(left, right int) bool {
		return len(secrets[left]) > len(secrets[right])
	})
	seen := make(map[string]struct{}, len(secrets))
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if _, ok := seen[secret]; ok {
			continue
		}
		seen[secret] = struct{}{}
		output = strings.ReplaceAll(output, secret, "[REDACTED]")
	}
	return sensitiveDiagnosticAssignment.ReplaceAllString(output, `${1}[REDACTED]`)
}

func truncateUTF8(value string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", value != ""
	}
	if len(value) <= maxBytes {
		return value, false
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value, true
}
