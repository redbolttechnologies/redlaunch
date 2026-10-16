package application

import (
	"sort"
	"time"
)

// RegistryVersion is one image in the managed local registry: a manifest
// digest with the tags currently assigned to it. Tags are movable pointers,
// so several tags can name the same version.
type RegistryVersion struct {
	Digest string
	Tags   []string
	// PushedAt is the newest tag assignment time, or the revision time for
	// an untagged image. It orders the versions listing and may be zero
	// when the time could not be read.
	PushedAt time.Time
}

// ServiceVersions is the version deployment state of one managed service.
// Repository is empty when the service does not deploy its image through the
// managed local registry, in which case no versions exist for it.
type ServiceVersions struct {
	ServiceName   string
	Repository    string
	DeploymentTag string
	// TaggedDigest is the image the deployment tag is assigned to right now.
	// It is empty when the tag has never been pushed.
	TaggedDigest string
	// RunningDigest is the image behind the running container. It is empty
	// when it could not be determined.
	RunningDigest     string
	Versions          []RegistryVersion
	RegistryAvailable bool
}

// Eligible reports whether the service deploys its image through the managed
// local registry and can therefore list and switch versions.
func (v ServiceVersions) Eligible() bool {
	return v.Repository != "" && v.DeploymentTag != ""
}

// DeployedDigest returns the image the service currently deploys: the running
// container's image when it is known, the deployment tag's target otherwise.
func (v ServiceVersions) DeployedDigest() string {
	if v.RunningDigest != "" {
		return v.RunningDigest
	}
	return v.TaggedDigest
}

// RestartPending reports that the deployment tag names a different image than
// the one the running container uses, so the tagged version still has to be
// applied.
func (v ServiceVersions) RestartPending() bool {
	return v.TaggedDigest != "" && v.RunningDigest != "" && v.TaggedDigest != v.RunningDigest
}

// ServiceVersionTarget is one service of an application that deploys its
// image through the managed local registry.
type ServiceVersionTarget struct {
	ServiceName   string
	Repository    string
	DeploymentTag string
}

// ApplicationVersionTag is one tag name found across an application's
// repositories. Services lists the services whose repository contains the
// tag and that can therefore deploy it; Missing lists the remaining eligible
// services, which are skipped by an application-wide deployment of this tag.
type ApplicationVersionTag struct {
	Name     string
	PushedAt time.Time
	Services []string
	Missing  []string
}

// ApplicationVersions is the data behind the application-wide version
// deployment: the services that deploy local-registry images and the tags
// available across their repositories, newest first.
type ApplicationVersions struct {
	Services []ServiceVersionTarget
	Tags     []ApplicationVersionTag
}

// Version deploy states for one service. Active is reported while a
// deployment is in progress; deployed, skipped and failed are the outcomes
// recorded in a VersionDeployOutcome.
const (
	VersionDeployStateActive   = "active"
	VersionDeployStateDeployed = "deployed"
	VersionDeployStateSkipped  = "skipped"
	VersionDeployStateFailed   = "failed"
)

// VersionDeployOutcome is one service's result in an application-wide version
// deployment. Detail is a short operator-facing explanation and never carries
// image or environment values beyond repository tags and digests.
type VersionDeployOutcome struct {
	ServiceName string
	State       string
	Detail      string
}

// GroupRegistryVersions groups one repository's tags and retained revisions by
// manifest digest. Tags on the same digest form one version, mirroring how
// the registry stores images independently of their movable tags. Versions
// are ordered newest first, with the digest as a stable tie-break.
func GroupRegistryVersions(images []RegistryImage, repository string) []RegistryVersion {
	byDigest := make(map[string]*RegistryVersion)
	for _, image := range images {
		if image.Repository != repository || image.Digest == "" {
			continue
		}
		version, ok := byDigest[image.Digest]
		if !ok {
			version = &RegistryVersion{Digest: image.Digest}
			byDigest[image.Digest] = version
		}
		if image.Tag != "" {
			version.Tags = append(version.Tags, image.Tag)
		}
		if image.PushedAt.After(version.PushedAt) {
			version.PushedAt = image.PushedAt
		}
	}
	versions := make([]RegistryVersion, 0, len(byDigest))
	for _, version := range byDigest {
		sort.Strings(version.Tags)
		versions = append(versions, *version)
	}
	sort.Slice(versions, func(left, right int) bool {
		leftZero := versions[left].PushedAt.IsZero()
		rightZero := versions[right].PushedAt.IsZero()
		if leftZero != rightZero {
			return rightZero
		}
		if !versions[left].PushedAt.Equal(versions[right].PushedAt) {
			return versions[left].PushedAt.After(versions[right].PushedAt)
		}
		return versions[left].Digest < versions[right].Digest
	})
	return versions
}

// DigestForTag returns the digest the given tag is assigned to within
// versions, or an empty string when the tag is not present.
func DigestForTag(versions []RegistryVersion, tag string) string {
	for _, version := range versions {
		for _, versionTag := range version.Tags {
			if versionTag == tag {
				return version.Digest
			}
		}
	}
	return ""
}

// GroupApplicationVersionTags lists the tags available across the
// repositories of the given services, newest first. Each entry records which
// services can deploy the tag and which eligible services are skipped by it.
func GroupApplicationVersionTags(images []RegistryImage, targets []ServiceVersionTarget) []ApplicationVersionTag {
	tagsByName := make(map[string]*ApplicationVersionTag)
	present := make(map[string]map[string]bool)
	for _, target := range targets {
		for _, image := range images {
			if image.Repository != target.Repository || image.Tag == "" {
				continue
			}
			tag, ok := tagsByName[image.Tag]
			if !ok {
				tag = &ApplicationVersionTag{Name: image.Tag}
				tagsByName[image.Tag] = tag
				present[image.Tag] = make(map[string]bool)
			}
			if image.PushedAt.After(tag.PushedAt) {
				tag.PushedAt = image.PushedAt
			}
			if !present[image.Tag][target.ServiceName] {
				present[image.Tag][target.ServiceName] = true
				tag.Services = append(tag.Services, target.ServiceName)
			}
		}
	}
	tags := make([]ApplicationVersionTag, 0, len(tagsByName))
	for name, tag := range tagsByName {
		sort.Strings(tag.Services)
		for _, target := range targets {
			if !present[name][target.ServiceName] {
				tag.Missing = append(tag.Missing, target.ServiceName)
			}
		}
		tags = append(tags, *tag)
	}
	sort.Slice(tags, func(left, right int) bool {
		leftZero := tags[left].PushedAt.IsZero()
		rightZero := tags[right].PushedAt.IsZero()
		if leftZero != rightZero {
			return rightZero
		}
		if !tags[left].PushedAt.Equal(tags[right].PushedAt) {
			return tags[left].PushedAt.After(tags[right].PushedAt)
		}
		return tags[left].Name < tags[right].Name
	})
	return tags
}
