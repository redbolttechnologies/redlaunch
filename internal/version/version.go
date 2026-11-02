// Package version contains build metadata injected by the release build.
package version

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Version is the release tag for tagged builds, or "dev" for local builds.
var Version = "dev"

// Commit is the source revision recorded by the release build.
var Commit = "unknown"

// BuildDate is the UTC build timestamp recorded by the release build.
var BuildDate = "unknown"

// DeploymentContract identifies the installation layout expected by this
// binary. A release may only update an installation whose contract matches.
const DeploymentContract = 1

// Info describes the running build.
type Info struct {
	Version            string `json:"version"`
	Commit             string `json:"commit"`
	BuildDate          string `json:"build_date"`
	DeploymentContract int    `json:"deployment_contract"`
}

// Current returns the build metadata for this process.
func Current() Info {
	return Info{
		Version:            normalizedVersion(Version),
		Commit:             strings.TrimSpace(Commit),
		BuildDate:          strings.TrimSpace(BuildDate),
		DeploymentContract: DeploymentContract,
	}
}

// IsDevelopment reports whether this is not a stable release build.
func IsDevelopment() bool {
	value := normalizedVersion(Version)
	if !strings.HasPrefix(value, "v") {
		return true
	}
	parts := strings.Split(value[1:], ".")
	if len(parts) != 3 {
		return true
	}
	for _, part := range parts {
		if part == "" {
			return true
		}
		if _, err := strconv.Atoi(part); err != nil {
			return true
		}
	}
	return false
}

// WriteJSON writes the build metadata as a single JSON object.
func WriteJSON(w io.Writer) error {
	encoder := json.NewEncoder(w)
	return encoder.Encode(Current())
}

// WriteText writes a compact human-readable version line.
func WriteText(w io.Writer) error {
	info := Current()
	_, err := fmt.Fprintf(w, "redlaunch %s (commit %s, built %s, deployment contract %d)\n",
		info.Version, info.Commit, info.BuildDate, info.DeploymentContract)
	return err
}

func normalizedVersion(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "dev"
	}
	return value
}
