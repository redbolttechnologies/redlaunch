package application

import (
	"errors"
)

// RegistryRetentionPlan describes the outcome of one repository's retention
// decision. DeleteOrder contains every digest that must be removed, parents
// before their children.
type RegistryRetentionPlan struct {
	Kept        []RegistryVersion
	Purged      []RegistryVersion
	DeleteOrder []RegistryVersion
	// PurgedVersions counts user-facing versions removed, excluding untagged
	// child manifests that belong to a multi-platform index.
	PurgedVersions int
}

// SelectRegistryVersions applies the shared retention policy to a repository's
// versions. Protected versions and versions with unknown push times are always
// kept and do not consume the keep quota. excluded identifies untagged child
// manifests whose lifecycle belongs to a retained index; those do not consume
// the quota either.
//
// A keep count of zero means unlimited.
func SelectRegistryVersions(versions []RegistryVersion, repository string, keep int, protected map[string]bool, excluded map[string]bool) (map[string]bool, error) {
	if _, err := ValidateRegistryKeepCount(keep); err != nil {
		return nil, err
	}
	keepSet := make(map[string]bool, len(versions))
	if keep == 0 {
		for _, version := range versions {
			keepSet[version.Digest] = true
		}
		return keepSet, nil
	}
	count := 0
	for _, version := range versions {
		if excluded[version.Digest] {
			continue
		}
		if version.PushedAt.IsZero() || registryVersionProtected(protected, repository, version) {
			keepSet[version.Digest] = true
			continue
		}
		if count < keep {
			keepSet[version.Digest] = true
			count++
		}
	}
	return keepSet, nil
}

// PlanRegistryRetention applies the shared policy and preserves the complete
// child manifest graph of every retained image index. All references must be
// present before a plan is returned.
func PlanRegistryRetention(versions []RegistryVersion, repository string, keep int, protected map[string]bool, references map[string][]string) (RegistryRetentionPlan, error) {
	children := make(map[string]bool)
	for _, refs := range references {
		for _, digest := range refs {
			children[digest] = true
		}
	}
	for _, version := range versions {
		for _, digest := range references[version.Digest] {
			if _, exists := references[digest]; !exists {
				return RegistryRetentionPlan{}, errors.New("registry manifest graph is incomplete")
			}
		}
	}
	excluded := make(map[string]bool)
	for _, version := range versions {
		if children[version.Digest] && len(version.Tags) == 0 {
			excluded[version.Digest] = true
		}
	}
	keepSet, err := SelectRegistryVersions(versions, repository, keep, protected, excluded)
	if err != nil {
		return RegistryRetentionPlan{}, err
	}

	visited := make(map[string]bool)
	var retainChildren func(string)
	retainChildren = func(digest string) {
		if visited[digest] {
			return
		}
		visited[digest] = true
		keepSet[digest] = true
		for _, child := range references[digest] {
			retainChildren(child)
		}
	}
	for digest := range keepSet {
		retainChildren(digest)
	}

	parents := make(map[string][]string)
	for _, version := range versions {
		for _, child := range references[version.Digest] {
			parents[child] = append(parents[child], version.Digest)
		}
	}
	ordered := make(map[string]bool)
	visiting := make(map[string]bool)
	deleteOrder := make([]RegistryVersion, 0)
	byDigest := make(map[string]RegistryVersion, len(versions))
	for _, version := range versions {
		byDigest[version.Digest] = version
	}
	var orderDeletion func(string) error
	orderDeletion = func(digest string) error {
		if keepSet[digest] || ordered[digest] {
			return nil
		}
		if visiting[digest] {
			return errors.New("registry manifest graph contains a cycle")
		}
		visiting[digest] = true
		for _, parent := range parents[digest] {
			if err := orderDeletion(parent); err != nil {
				return err
			}
		}
		visiting[digest] = false
		ordered[digest] = true
		if version, ok := byDigest[digest]; ok {
			deleteOrder = append(deleteOrder, version)
		}
		return nil
	}
	for _, version := range versions {
		if err := orderDeletion(version.Digest); err != nil {
			return RegistryRetentionPlan{}, err
		}
	}

	plan := RegistryRetentionPlan{
		Kept:        make([]RegistryVersion, 0, len(versions)),
		Purged:      make([]RegistryVersion, 0),
		DeleteOrder: deleteOrder,
	}
	for _, version := range versions {
		if keepSet[version.Digest] {
			plan.Kept = append(plan.Kept, version)
			continue
		}
		plan.Purged = append(plan.Purged, version)
		if children[version.Digest] && len(version.Tags) == 0 {
			continue
		}
		plan.PurgedVersions++
	}
	return plan, nil
}

// PreviewRegistryRetention applies the shared selection policy without manifest
// graph information. The result is advisory: execution may keep more versions
// when child manifests or runtime protections are discovered.
func PreviewRegistryRetention(images []RegistryImage, repository string, keep int, protected map[string]bool) ([]RegistryVersion, []RegistryVersion, error) {
	versions := GroupRegistryVersions(images, repository)
	keepSet, err := SelectRegistryVersions(versions, repository, keep, protected, nil)
	if err != nil {
		return nil, nil, err
	}
	kept := make([]RegistryVersion, 0, len(versions))
	purged := make([]RegistryVersion, 0)
	for _, version := range versions {
		if keepSet[version.Digest] {
			kept = append(kept, version)
			continue
		}
		purged = append(purged, version)
	}
	return kept, purged, nil
}

func registryVersionProtected(protected map[string]bool, repository string, version RegistryVersion) bool {
	if protected[RegistryImageKey(repository, version.Digest)] {
		return true
	}
	for _, tag := range version.Tags {
		if protected[RegistryImageKey(repository, tag)] {
			return true
		}
	}
	return false
}
