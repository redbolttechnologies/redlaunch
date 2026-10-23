package application

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

const (
	// MaxOneOffRunArguments is the maximum number of positional arguments a
	// single parameterized one-off run may pass to the service.
	MaxOneOffRunArguments = 64
	// MaxOneOffRunArgumentBytes bounds one positional argument.
	MaxOneOffRunArgumentBytes = 4096
	// MaxOneOffRunArgumentsBytes bounds all positional arguments together.
	MaxOneOffRunArgumentsBytes = 64 << 10
	// MaxOneOffRunEnvironmentEntries bounds per-run environment overrides.
	MaxOneOffRunEnvironmentEntries = 16
	// MaxOneOffRunEnvironmentValueBytes bounds one environment value.
	MaxOneOffRunEnvironmentValueBytes = 4096
	// MaxOneOffRunEnvironmentBytes bounds all overrides together, including
	// their keys.
	MaxOneOffRunEnvironmentBytes = 32 << 10
	// MaxOneOffRunRequestBytes bounds the HTTP request body carrying run
	// parameters. It is deliberately larger than the parameter limits so
	// form/JSON framing overhead is not mistaken for a parameter violation.
	MaxOneOffRunRequestBytes = 64 << 10
)

var (
	ErrOneOffRunParametersInvalid        = errors.New("one-off run parameters are invalid")
	ErrOneOffRunEnvironmentKeyNotAllowed = errors.New("one-off run environment key is not allowed")
)

// OneOffRunParameters contains per-run arguments and environment overrides for
// a registered service. They apply only to the one-off container and are never
// written to the service definition.
type OneOffRunParameters struct {
	Args []string          `json:"args,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
}

// OneOffRunResult is the captured outcome of a one-off service run.
type OneOffRunResult struct {
	ExitCode  int           `json:"exit_code"`
	Stdout    string        `json:"stdout"`
	Stderr    string        `json:"stderr"`
	Truncated bool          `json:"truncated"`
	Duration  time.Duration `json:"duration"`
}

var oneOffRunEnvironmentAllowlist = map[string]struct{}{
	"NODE_ENV":  {},
	"TZ":        {},
	"LANG":      {},
	"LC_ALL":    {},
	"LOG_LEVEL": {},
}

// OneOffRunEnvironmentKeys returns the environment keys an operator may
// override for a single run. The allowlist is intentionally small: execution
// affecting variables such as PATH, LD_PRELOAD, NODE_OPTIONS, and BASH_ENV must
// never be accepted from an HTTP or MCP caller.
func OneOffRunEnvironmentKeys() []string {
	keys := make([]string, 0, len(oneOffRunEnvironmentAllowlist))
	for key := range oneOffRunEnvironmentAllowlist {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// IsOneOffRunEnvironmentKeyAllowed reports whether key is in the explicit
// per-run environment allowlist.
func IsOneOffRunEnvironmentKeyAllowed(key string) bool {
	_, ok := oneOffRunEnvironmentAllowlist[key]
	return ok
}

// ValidateOneOffRunParameters validates and defensively copies per-run
// parameters. Values are never included in returned errors.
func ValidateOneOffRunParameters(parameters OneOffRunParameters) (OneOffRunParameters, error) {
	if len(parameters.Args) > MaxOneOffRunArguments {
		return OneOffRunParameters{}, fmt.Errorf("%w: at most %d arguments are allowed", ErrOneOffRunParametersInvalid, MaxOneOffRunArguments)
	}
	argumentBytes := 0
	args := make([]string, len(parameters.Args))
	for index, argument := range parameters.Args {
		if len(argument) > MaxOneOffRunArgumentBytes {
			return OneOffRunParameters{}, fmt.Errorf("%w: argument %d exceeds %d bytes", ErrOneOffRunParametersInvalid, index+1, MaxOneOffRunArgumentBytes)
		}
		argumentBytes += len(argument)
		if argumentBytes > MaxOneOffRunArgumentsBytes {
			return OneOffRunParameters{}, fmt.Errorf("%w: arguments exceed %d bytes in total", ErrOneOffRunParametersInvalid, MaxOneOffRunArgumentsBytes)
		}
		if containsOneOffRunControlCharacter(argument) {
			return OneOffRunParameters{}, fmt.Errorf("%w: argument %d contains a control character", ErrOneOffRunParametersInvalid, index+1)
		}
		args[index] = argument
	}

	if len(parameters.Env) > MaxOneOffRunEnvironmentEntries {
		return OneOffRunParameters{}, fmt.Errorf("%w: at most %d environment entries are allowed", ErrOneOffRunParametersInvalid, MaxOneOffRunEnvironmentEntries)
	}
	environment := make(map[string]string, len(parameters.Env))
	environmentBytes := 0
	environmentKeys := make([]string, 0, len(parameters.Env))
	for key := range parameters.Env {
		environmentKeys = append(environmentKeys, key)
	}
	sort.Strings(environmentKeys)
	for _, key := range environmentKeys {
		value := parameters.Env[key]
		if !validOneOffRunEnvironmentName(key) {
			return OneOffRunParameters{}, fmt.Errorf("%w: environment key %q is invalid", ErrOneOffRunParametersInvalid, safeEnvironmentKey(key))
		}
		if !IsOneOffRunEnvironmentKeyAllowed(key) {
			return OneOffRunParameters{}, fmt.Errorf("%w: %q (allowed: %v)", ErrOneOffRunEnvironmentKeyNotAllowed, safeEnvironmentKey(key), OneOffRunEnvironmentKeys())
		}
		if len(value) > MaxOneOffRunEnvironmentValueBytes {
			return OneOffRunParameters{}, fmt.Errorf("%w: value for %q exceeds %d bytes", ErrOneOffRunParametersInvalid, key, MaxOneOffRunEnvironmentValueBytes)
		}
		environmentBytes += len(key) + len(value)
		if environmentBytes > MaxOneOffRunEnvironmentBytes {
			return OneOffRunParameters{}, fmt.Errorf("%w: environment overrides exceed %d bytes in total", ErrOneOffRunParametersInvalid, MaxOneOffRunEnvironmentBytes)
		}
		if containsOneOffRunControlCharacter(value) {
			return OneOffRunParameters{}, fmt.Errorf("%w: value for %q contains a control character", ErrOneOffRunParametersInvalid, key)
		}
		environment[key] = value
	}
	return OneOffRunParameters{Args: args, Env: environment}, nil
}

func validOneOffRunEnvironmentName(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		if index == 0 {
			if character != '_' && !isASCIIAlpha(character) {
				return false
			}
			continue
		}
		if character != '_' && !isASCIIAlphaNumeric(character) {
			return false
		}
	}
	return true
}

func containsOneOffRunControlCharacter(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

// safeEnvironmentKey keeps validation errors printable without echoing a
// hostile key verbatim. Keys are already limited to the allowlist when this is
// reached, but the helper also protects the invalid-name branch.
func safeEnvironmentKey(key string) string {
	const max = 64
	printable := make([]rune, 0, len(key))
	for _, character := range key {
		if character < 0x20 || character == 0x7f {
			printable = append(printable, '?')
			continue
		}
		printable = append(printable, character)
		if len(printable) == max {
			break
		}
	}
	return string(printable)
}
