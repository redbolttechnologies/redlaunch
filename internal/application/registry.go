// Package application contains the domain types and validation for managed
// Docker Compose applications.
package application

import (
	"sort"
	"strings"
)

// RegistryRetentionPolicy is the per-repository keep setting. KeepCount 0
// means unlimited for that repository; otherwise 1..MaxRegistryKeepCount
// images are kept.
type RegistryRetentionPolicy struct {
	Repository string
	KeepCount  int
}

// ValidateRegistryKeepCount accepts 0 (unlimited) through
// MaxRegistryKeepCount.
func ValidateRegistryKeepCount(value int) (int, error) {
	if value < 0 || value > MaxRegistryKeepCount {
		return 0, ErrRegistryKeepInvalid
	}
	return value, nil
}

// ValidateRegistryRepository normalizes one registry repository path such as
// acme-app or status-page/web. It mirrors the Docker distribution path rules
// used by the Compose adapter without importing infrastructure code.
func ValidateRegistryRepository(value string) (string, error) {
	repository := strings.TrimSpace(value)
	if repository == "" || len(repository) > 255 {
		return "", ErrRegistryRepositoryInvalid
	}
	for _, part := range strings.Split(repository, "/") {
		if !validRegistryRepositoryPart(part) {
			return "", ErrRegistryRepositoryInvalid
		}
	}
	return repository, nil
}

func validRegistryRepositoryPart(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		lowerAlphanumeric := character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
		if index == 0 || index == len(value)-1 {
			if !lowerAlphanumeric {
				return false
			}
			continue
		}
		if !lowerAlphanumeric && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

// ValidateRegistryTag accepts a Docker distribution tag for purge requests.
func ValidateRegistryTag(value string) (string, error) {
	tag := strings.TrimSpace(value)
	if tag == "" || len(tag) > 128 {
		return "", ErrRegistryRepositoryInvalid
	}
	for index, character := range tag {
		word := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_'
		if index == 0 {
			if !word {
				return "", ErrRegistryRepositoryInvalid
			}
			continue
		}
		if !word && character != '.' && character != '-' {
			return "", ErrRegistryRepositoryInvalid
		}
	}
	return tag, nil
}

// RegistryImageKey identifies one repository tag for deployed-reference
// protection.
func RegistryImageKey(repository, tag string) string {
	return repository + "\x00" + tag
}

// ParseDeployedRegistryReference extracts the repository and tag from a
// managed service image such as localhost:5000/acme-app:abc123. It reports
// false for non-registry images.
func ParseDeployedRegistryReference(imageName string) (repository, tag string, ok bool) {
	imageName = strings.TrimSpace(imageName)
	prefix := LocalRegistryAddress + "/"
	if !strings.HasPrefix(imageName, prefix) {
		return "", "", false
	}
	reference := strings.TrimPrefix(imageName, prefix)
	repository, tag, ok = strings.Cut(reference, ":")
	if !ok {
		return "", "", false
	}
	if _, err := ValidateRegistryRepository(repository); err != nil {
		return "", "", false
	}
	if _, err := ValidateRegistryTag(tag); err != nil {
		return "", "", false
	}
	return repository, tag, true
}

// EffectiveRegistryKeep resolves the keep count for one repository: an
// explicit per-repository policy wins, otherwise the global default applies.
// Both values accept 0 for unlimited.
func EffectiveRegistryKeep(defaultKeep int, policies map[string]int, repository string) int {
	if policies != nil {
		if keep, ok := policies[repository]; ok {
			return keep
		}
	}
	return defaultKeep
}

// PlanRegistryPurge splits one repository's images into kept and purge
// candidates. Images are ordered newest first by PushedAt; images without a
// timestamp sort oldest with a lexical tie-break. Protected references (for
// example currently deployed tags) are always kept and do not consume the
// keep quota. A keep of 0 means unlimited and purges nothing.
func PlanRegistryPurge(images []RegistryImage, keep int, protected map[string]bool) (kept, purge []RegistryImage, err error) {
	if _, err := ValidateRegistryKeepCount(keep); err != nil {
		return nil, nil, err
	}
	sorted := append([]RegistryImage(nil), images...)
	sort.Slice(sorted, func(left, right int) bool {
		leftZero := sorted[left].PushedAt.IsZero()
		rightZero := sorted[right].PushedAt.IsZero()
		if leftZero != rightZero {
			return rightZero
		}
		if !sorted[left].PushedAt.Equal(sorted[right].PushedAt) {
			return sorted[left].PushedAt.After(sorted[right].PushedAt)
		}
		if sorted[left].Tag != sorted[right].Tag {
			return sorted[left].Tag < sorted[right].Tag
		}
		return sorted[left].Digest < sorted[right].Digest
	})
	if keep == 0 {
		return sorted, nil, nil
	}
	kept = make([]RegistryImage, 0, len(sorted))
	purge = make([]RegistryImage, 0)
	keptUnprotected := 0
	for _, image := range sorted {
		if protected[RegistryImageKey(image.Repository, image.Tag)] {
			kept = append(kept, image)
			continue
		}
		if keptUnprotected < keep {
			kept = append(kept, image)
			keptUnprotected++
			continue
		}
		purge = append(purge, image)
	}
	if kept == nil {
		kept = []RegistryImage{}
	}
	if purge == nil {
		purge = []RegistryImage{}
	}
	return kept, purge, nil
}
