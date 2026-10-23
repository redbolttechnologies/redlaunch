package application

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateOneOffRunParametersAcceptsAllowlistedValues(t *testing.T) {
	input := OneOffRunParameters{
		Args: []string{"--database-name", "example name", "--confirm"},
		Env: map[string]string{
			"NODE_ENV":  "development",
			"TZ":        "UTC",
			"LANG":      "en_US.UTF-8",
			"LC_ALL":    "en_US.UTF-8",
			"LOG_LEVEL": "debug",
		},
	}
	got, err := ValidateOneOffRunParameters(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Args, "\x00") != strings.Join(input.Args, "\x00") {
		t.Fatalf("arguments = %#v, want ordered copy %#v", got.Args, input.Args)
	}
	for key, value := range input.Env {
		if got.Env[key] != value {
			t.Fatalf("environment[%q] = %q, want %q", key, got.Env[key], value)
		}
	}

	input.Args[0] = "mutated"
	input.Env["NODE_ENV"] = "mutated"
	if got.Args[0] != "--database-name" || got.Env["NODE_ENV"] != "development" {
		t.Fatal("validated parameters alias caller-owned values")
	}
}

func TestValidateOneOffRunParametersRejectsLimitsAndControlCharacters(t *testing.T) {
	tooManyArgs := make([]string, MaxOneOffRunArguments+1)
	largeArgs := make([]string, 17)
	for index := range largeArgs {
		largeArgs[index] = strings.Repeat("a", MaxOneOffRunArgumentBytes)
	}
	tooManyEnv := map[string]string{}
	for index := 0; index < MaxOneOffRunEnvironmentEntries+1; index++ {
		tooManyEnv["UNKNOWN_"+strings.Repeat("A", index+1)] = "value"
	}
	largeEnv := map[string]string{
		"NODE_ENV":  strings.Repeat("a", MaxOneOffRunEnvironmentValueBytes+1),
		"TZ":        "UTC",
		"LANG":      "C",
		"LC_ALL":    "C",
		"LOG_LEVEL": strings.Repeat("b", MaxOneOffRunEnvironmentValueBytes),
	}
	cases := []struct {
		name       string
		parameters OneOffRunParameters
	}{
		{"too many arguments", OneOffRunParameters{Args: tooManyArgs}},
		{"argument too long", OneOffRunParameters{Args: []string{strings.Repeat("a", MaxOneOffRunArgumentBytes+1)}}},
		{"arguments too large", OneOffRunParameters{Args: largeArgs}},
		{"NUL argument", OneOffRunParameters{Args: []string{"before\x00after"}}},
		{"CR argument", OneOffRunParameters{Args: []string{"before\rafter"}}},
		{"LF argument", OneOffRunParameters{Args: []string{"before\nafter"}}},
		{"too many environment entries", OneOffRunParameters{Env: tooManyEnv}},
		{"environment value too long", OneOffRunParameters{Env: largeEnv}},
		{"unknown environment key", OneOffRunParameters{Env: map[string]string{"PATH": "/tmp"}}},
		{"invalid environment key", OneOffRunParameters{Env: map[string]string{"NOT-A-KEY": "value"}}},
		{"empty environment key", OneOffRunParameters{Env: map[string]string{"": "value"}}},
		{"NUL environment value", OneOffRunParameters{Env: map[string]string{"NODE_ENV": "before\x00after"}}},
		{"CR environment value", OneOffRunParameters{Env: map[string]string{"NODE_ENV": "before\rafter"}}},
		{"LF environment value", OneOffRunParameters{Env: map[string]string{"NODE_ENV": "before\nafter"}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := ValidateOneOffRunParameters(testCase.parameters); !errors.Is(err, ErrOneOffRunParametersInvalid) && !errors.Is(err, ErrOneOffRunEnvironmentKeyNotAllowed) {
				t.Fatalf("ValidateOneOffRunParameters() error = %v, want parameter validation error", err)
			}
		})
	}
}

func TestOneOffRunEnvironmentAllowlistIsExplicit(t *testing.T) {
	want := []string{"LANG", "LC_ALL", "LOG_LEVEL", "NODE_ENV", "TZ"}
	if got := OneOffRunEnvironmentKeys(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("OneOffRunEnvironmentKeys() = %v, want %v", got, want)
	}
	for _, forbidden := range []string{"PATH", "LD_PRELOAD", "LD_LIBRARY_PATH", "NODE_OPTIONS", "BASH_ENV", "ENV", "PYTHONPATH", "RUBYOPT", "PERL5OPT"} {
		if IsOneOffRunEnvironmentKeyAllowed(forbidden) {
			t.Fatalf("execution-affecting key %q is allowed", forbidden)
		}
	}
}
