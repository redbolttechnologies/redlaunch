package application

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrReleaseUnavailable reports that the release feed or a required asset
	// could not be read.
	ErrReleaseUnavailable = errors.New("Redlaunch release information is unavailable")
	// ErrReleaseIncompatible reports that a release requires a different
	// installation layout or deployment contract.
	ErrReleaseIncompatible = errors.New("the release is not compatible with this installation")
	// ErrUpdateBusy reports that another installation operation is running.
	ErrUpdateBusy = errors.New("another Redlaunch installation operation is running")
	// ErrUpdateUncertain reports that the outcome of an installation operation
	// could not be established safely.
	ErrUpdateUncertain = errors.New("the Redlaunch update outcome is uncertain")
	// ErrUpdateUnavailable reports that release-based updates are disabled for
	// this build or installation.
	ErrUpdateUnavailable = errors.New("release-based updates are unavailable")
)

// ReleaseAsset is one downloadable file attached to a GitHub release.
type ReleaseAsset struct {
	Name string
	URL  string
	Size int64
}

// ReleaseImage describes the image archive for one architecture.
type ReleaseImage struct {
	Arch    string `json:"arch"`
	Asset   string `json:"asset"`
	SHA256  string `json:"sha256"`
	ImageID string `json:"image_id"`
	Tag     string `json:"tag"`
}

// ReleaseManifest is the machine-readable Redlaunch release contract.
type ReleaseManifest struct {
	Version            string                  `json:"version"`
	Commit             string                  `json:"commit"`
	DeploymentContract int                     `json:"deployment_contract"`
	Images             map[string]ReleaseImage `json:"images"`
}

// ReleaseInfo is a published GitHub release and its Redlaunch manifest.
type ReleaseInfo struct {
	Version     string
	HTMLURL     string
	PublishedAt time.Time
	Manifest    ReleaseManifest
	Assets      []ReleaseAsset
	Checksums   map[string]string
}

// UpdateStatus is the result of one release check.
type UpdateStatus struct {
	CurrentVersion   string
	LatestVersion    string
	LatestURL        string
	PublishedAt      time.Time
	UpdateAvailable  bool
	Compatible       bool
	DevelopmentBuild bool
	Message          string
	Release          *ReleaseInfo
}

// UpdateStatePhase tracks a durable installation operation.
type UpdateStatePhase string

const (
	UpdatePhasePreparing     UpdateStatePhase = "preparing"
	UpdatePhaseDownloading   UpdateStatePhase = "downloading"
	UpdatePhaseVerified      UpdateStatePhase = "verified"
	UpdatePhaseLoaded        UpdateStatePhase = "loaded"
	UpdatePhaseRestartQueued UpdateStatePhase = "restart-queued"
	UpdatePhaseRecreating    UpdateStatePhase = "recreating"
	UpdatePhaseVerifying     UpdateStatePhase = "verifying"
	UpdatePhaseComplete      UpdateStatePhase = "complete"
	UpdatePhaseFailed        UpdateStatePhase = "failed"
	UpdatePhaseUncertain     UpdateStatePhase = "uncertain"
)

// UpdateState is the persisted, non-secret record of one installation
// operation. It must remain small enough to render directly in the UI.
type UpdateState struct {
	Schema             int              `json:"schema"`
	Revision           int              `json:"revision"`
	OperationID        string           `json:"operation_id"`
	Owner              string           `json:"owner"`
	Type               string           `json:"type"`
	Phase              UpdateStatePhase `json:"phase"`
	Version            string           `json:"version"`
	Commit             string           `json:"commit"`
	DeploymentContract int              `json:"deployment_contract"`
	PreviousImageID    string           `json:"previous_image_id"`
	TargetImageID      string           `json:"target_image_id"`
	RuntimeImage       string           `json:"runtime_image"`
	InstallDir         string           `json:"install_dir"`
	ComposeFile        string           `json:"compose_file"`
	ProjectName        string           `json:"project_name"`
	ManagerContainer   string           `json:"manager_container"`
	HelperName         string           `json:"helper_name"`
	DownloadPath       string           `json:"download_path,omitempty"`
	StartedAt          time.Time        `json:"started_at"`
	UpdatedAt          time.Time        `json:"updated_at"`
	FinishedAt         time.Time        `json:"finished_at,omitempty"`
	Error              string           `json:"error,omitempty"`
	Notified           bool             `json:"notified,omitempty"`
	Acknowledged       bool             `json:"acknowledged,omitempty"`
}

// Terminal reports whether the state no longer represents active work.
func (s UpdateState) Terminal() bool {
	return s.Phase == UpdatePhaseComplete || s.Phase == UpdatePhaseFailed || s.Phase == UpdatePhaseUncertain
}

// ParseReleaseVersion parses a stable vMAJOR.MINOR.PATCH release tag.
func ParseReleaseVersion(value string) ([3]int, error) {
	var result [3]int
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "v") {
		return result, fmt.Errorf("release version %q must start with v", value)
	}
	parts := strings.Split(value[1:], ".")
	if len(parts) != 3 {
		return result, fmt.Errorf("release version %q must have three components", value)
	}
	for index, part := range parts {
		if part == "" || strings.HasPrefix(part, "+") || strings.HasPrefix(part, "-") {
			return result, fmt.Errorf("release version %q is not stable semantic versioning", value)
		}
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return result, fmt.Errorf("release version %q is invalid", value)
		}
		result[index] = number
	}
	return result, nil
}

// CompareReleaseVersions compares two stable release tags.
func CompareReleaseVersions(left, right string) (int, error) {
	leftParsed, err := ParseReleaseVersion(left)
	if err != nil {
		return 0, err
	}
	rightParsed, err := ParseReleaseVersion(right)
	if err != nil {
		return 0, err
	}
	for index := range leftParsed {
		switch {
		case leftParsed[index] < rightParsed[index]:
			return -1, nil
		case leftParsed[index] > rightParsed[index]:
			return 1, nil
		}
	}
	return 0, nil
}

// IsNewerRelease reports whether candidate is newer than current. It returns
// false and an error when either value is not a stable release tag.
func IsNewerRelease(current, candidate string) (bool, error) {
	comparison, err := CompareReleaseVersions(candidate, current)
	if err != nil {
		return false, err
	}
	return comparison > 0, nil
}
