// Package application contains the domain types and validation for managed
// Docker Compose applications.
package application

// ComposeImportServicePreview summarizes one Compose service found in an
// uploaded file. Settings are the values Redlaunch reads during import.
type ComposeImportServicePreview struct {
	Name            string
	Image           string
	DetectedType    string
	DetectionReason string
	ContainerName   string
	Restart         string
	Command         string
	Build           string
	Ports           []string
	Volumes         []string
	Networks        []string
	EnvFiles        []string
	Environment     []string
	DependsOn       []string
}

// ComposeImportResourcePreview summarizes one top-level volume or network.
// Config holds the trimmed setting lines for that resource (without the
// resource name). UsedBy names the services that reference the resource.
type ComposeImportResourcePreview struct {
	Name   string
	Config []string
	UsedBy []string
}

// ComposeImportPreview is the read-only summary shown before an import.
type ComposeImportPreview struct {
	Services []ComposeImportServicePreview
	Volumes  []ComposeImportResourcePreview
	Networks []ComposeImportResourcePreview
}

// ComposeImportSelection names the subset of a Compose file to import. When
// Selective is false the whole file is imported.
type ComposeImportSelection struct {
	Services  []string
	Volumes   []string
	Networks  []string
	Selective bool
}
