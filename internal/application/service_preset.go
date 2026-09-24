package application

import (
	"strings"
)

// Service preset categories group marketplace entries for browsing. Slugs are
// stable identifiers used by templates and client-side filtering; labels are
// display-only.
const (
	ServicePresetCategoryDatabase      = "database"
	ServicePresetCategoryObservability = "observability"
	ServicePresetCategoryStorage       = "storage"
	ServicePresetCategoryMessaging     = "messaging"
	ServicePresetCategorySearch        = "search"
)

// ServicePresetCategory describes one browsable catalog group.
type ServicePresetCategory struct {
	Slug  string
	Label string
}

// ServicePresetCategories returns catalog groups in display order.
func ServicePresetCategories() []ServicePresetCategory {
	return []ServicePresetCategory{
		{Slug: ServicePresetCategoryDatabase, Label: "Database"},
		{Slug: ServicePresetCategoryObservability, Label: "Observability"},
		{Slug: ServicePresetCategoryStorage, Label: "Storage"},
		{Slug: ServicePresetCategoryMessaging, Label: "Messaging"},
		{Slug: ServicePresetCategorySearch, Label: "Search"},
	}
}

// ServicePresetCategoryLabel resolves a category slug to its display label.
// Unknown slugs fall back to the slug itself so templates never render empty.
func ServicePresetCategoryLabel(slug string) string {
	for _, category := range ServicePresetCategories() {
		if category.Slug == slug {
			return category.Label
		}
	}
	if slug == "" {
		return "Other"
	}
	return slug
}

// ServicePreset is a preconfigured container definition used to prefill the
// general application container form. Presets intentionally only use fields
// the container form already supports; environment entries that require
// secrets are documented in EnvNote and left for the operator to add via
// vars.env/secrets.env.
type ServicePreset struct {
	Slug               string
	Name               string
	Description        string
	Category           string
	Image              string
	UseDockerRegistry  bool
	DefaultServiceName string
	PortMappings       []ApplicationPortMapping
	VolumeMappings     []ApplicationVolumeMapping
	Entrypoint         string
	RestartPolicy      string
	EnvNote            string
}

// AllServicePresets returns the curated marketplace catalog in display order.
func AllServicePresets() []ServicePreset {
	return []ServicePreset{
		{
			Slug:               "pgadmin",
			Name:               "pgAdmin",
			Description:        "PostgreSQL administration UI. Connect it to your database service.",
			Category:           ServicePresetCategoryDatabase,
			Image:              "dpage/pgadmin4:8",
			UseDockerRegistry:  true,
			DefaultServiceName: "pgadmin",
			PortMappings: []ApplicationPortMapping{
				{HostPort: "8080", ContainerPort: "80", Protocol: "tcp"},
			},
			VolumeMappings: []ApplicationVolumeMapping{
				{Source: "pgadmin-data", Target: "/var/lib/pgadmin", Options: "rw"},
			},
			RestartPolicy: ApplicationRestartPolicyUnlessStopped,
			EnvNote:       "Set PGADMIN_DEFAULT_EMAIL and PGADMIN_DEFAULT_PASSWORD in variables/secrets after creation.",
		},
		{
			Slug:               "mariadb",
			Name:               "MariaDB",
			Description:        "MySQL-compatible relational database.",
			Category:           ServicePresetCategoryDatabase,
			Image:              "mariadb:11.4",
			UseDockerRegistry:  true,
			DefaultServiceName: "mariadb",
			PortMappings: []ApplicationPortMapping{
				{HostPort: "3306", ContainerPort: "3306", Protocol: "tcp"},
			},
			VolumeMappings: []ApplicationVolumeMapping{
				{Source: "mariadb-data", Target: "/var/lib/mysql", Options: "rw"},
			},
			RestartPolicy: ApplicationRestartPolicyUnlessStopped,
			EnvNote:       "Set MARIADB_ROOT_PASSWORD in secrets before starting.",
		},
		{
			Slug:               "seq",
			Name:               "Seq",
			Description:        "Centralized structured log server with a web UI.",
			Category:           ServicePresetCategoryObservability,
			Image:              "datalust/seq:2024.1",
			UseDockerRegistry:  true,
			DefaultServiceName: "seq",
			PortMappings: []ApplicationPortMapping{
				{HostPort: "8080", ContainerPort: "80", Protocol: "tcp"},
			},
			VolumeMappings: []ApplicationVolumeMapping{
				{Source: "seq-data", Target: "/data", Options: "rw"},
			},
			RestartPolicy: ApplicationRestartPolicyUnlessStopped,
			EnvNote:       "Set ACCEPT_EULA=Y in variables to accept the Seq license.",
		},
		{
			Slug:               "grafana",
			Name:               "Grafana",
			Description:        "Metrics dashboards and alerting.",
			Category:           ServicePresetCategoryObservability,
			Image:              "grafana/grafana:11.0.0",
			UseDockerRegistry:  true,
			DefaultServiceName: "grafana",
			PortMappings: []ApplicationPortMapping{
				{HostPort: "3000", ContainerPort: "3000", Protocol: "tcp"},
			},
			VolumeMappings: []ApplicationVolumeMapping{
				{Source: "grafana-data", Target: "/var/lib/grafana", Options: "rw"},
			},
			RestartPolicy: ApplicationRestartPolicyUnlessStopped,
		},
		{
			Slug:               "prometheus",
			Name:               "Prometheus",
			Description:        "Metrics collection and alerting toolkit.",
			Category:           ServicePresetCategoryObservability,
			Image:              "prom/prometheus:v2.53.0",
			UseDockerRegistry:  true,
			DefaultServiceName: "prometheus",
			PortMappings: []ApplicationPortMapping{
				{HostPort: "9090", ContainerPort: "9090", Protocol: "tcp"},
			},
			VolumeMappings: []ApplicationVolumeMapping{
				{Source: "prometheus-data", Target: "/prometheus", Options: "rw"},
			},
			RestartPolicy: ApplicationRestartPolicyUnlessStopped,
		},
		{
			Slug:               "minio",
			Name:               "MinIO",
			Description:        "S3-compatible object storage with a web console.",
			Category:           ServicePresetCategoryStorage,
			Image:              "minio/minio:RELEASE.2024-06-13T22-53-53Z",
			UseDockerRegistry:  true,
			DefaultServiceName: "minio",
			PortMappings: []ApplicationPortMapping{
				{HostPort: "9000", ContainerPort: "9000", Protocol: "tcp"},
				{HostPort: "9001", ContainerPort: "9001", Protocol: "tcp"},
			},
			VolumeMappings: []ApplicationVolumeMapping{
				{Source: "minio-data", Target: "/data", Options: "rw"},
			},
			Entrypoint:    "server /data --console-address :9001",
			RestartPolicy: ApplicationRestartPolicyUnlessStopped,
			EnvNote:       "Set MINIO_ROOT_USER and MINIO_ROOT_PASSWORD in secrets before starting.",
		},
		{
			Slug:               "rabbitmq",
			Name:               "RabbitMQ",
			Description:        "Message broker with a management UI.",
			Category:           ServicePresetCategoryMessaging,
			Image:              "rabbitmq:3-management",
			UseDockerRegistry:  true,
			DefaultServiceName: "rabbitmq",
			PortMappings: []ApplicationPortMapping{
				{HostPort: "5672", ContainerPort: "5672", Protocol: "tcp"},
				{HostPort: "15672", ContainerPort: "15672", Protocol: "tcp"},
			},
			VolumeMappings: []ApplicationVolumeMapping{
				{Source: "rabbitmq-data", Target: "/var/lib/rabbitmq", Options: "rw"},
			},
			RestartPolicy: ApplicationRestartPolicyUnlessStopped,
		},
		{
			Slug:               "nats",
			Name:               "NATS",
			Description:        "Lightweight messaging with JetStream support.",
			Category:           ServicePresetCategoryMessaging,
			Image:              "nats:2.10",
			UseDockerRegistry:  true,
			DefaultServiceName: "nats",
			PortMappings: []ApplicationPortMapping{
				{HostPort: "4222", ContainerPort: "4222", Protocol: "tcp"},
				{HostPort: "8222", ContainerPort: "8222", Protocol: "tcp"},
			},
			VolumeMappings: []ApplicationVolumeMapping{
				{Source: "nats-data", Target: "/data", Options: "rw"},
			},
			RestartPolicy: ApplicationRestartPolicyUnlessStopped,
		},
		{
			Slug:               "meilisearch",
			Name:               "Meilisearch",
			Description:        "Fast full-text search API with a web UI.",
			Category:           ServicePresetCategorySearch,
			Image:              "getmeili/meilisearch:v1.6",
			UseDockerRegistry:  true,
			DefaultServiceName: "meilisearch",
			PortMappings: []ApplicationPortMapping{
				{HostPort: "7700", ContainerPort: "7700", Protocol: "tcp"},
			},
			VolumeMappings: []ApplicationVolumeMapping{
				{Source: "meili-data", Target: "/meili_data", Options: "rw"},
			},
			RestartPolicy: ApplicationRestartPolicyUnlessStopped,
			EnvNote:       "Set MEILI_MASTER_KEY in secrets for production use.",
		},
	}
}

// FindServicePreset returns the catalog entry for a query value. Lookup is
// case-insensitive and trims whitespace; unknown values return false so
// callers can fall back to blank container defaults.
func FindServicePreset(value string) (ServicePreset, bool) {
	slug := strings.ToLower(strings.TrimSpace(value))
	if slug == "" {
		return ServicePreset{}, false
	}
	for _, preset := range AllServicePresets() {
		if preset.Slug == slug {
			return preset, true
		}
	}
	return ServicePreset{}, false
}
