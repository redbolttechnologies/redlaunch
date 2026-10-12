package compose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"

	"redlaunch/internal/application"
)

var dockerVolumeNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// serviceVolumeCandidates excludes bind mounts, external volumes, and volumes
// referenced by another service (including aliases of the same Docker name).
func serviceVolumeCandidates(output []byte, project, service string) ([]string, error) {
	var config struct {
		Services map[string]struct {
			Volumes []struct{ Type, Source string }
		}
		Volumes map[string]struct {
			Name     string
			External json.RawMessage
		}
	}
	if err := json.Unmarshal(output, &config); err != nil {
		return nil, errors.New("decode Compose volume configuration")
	}
	if _, ok := config.Services[service]; !ok {
		return nil, application.ErrServiceNotFound
	}
	externalNames := map[string]bool{}
	for key, volume := range config.Volumes {
		if isExternalVolume(volume.External) {
			name := volume.Name
			if name == "" {
				name = project + "_" + key
			}
			externalNames[name] = true
		}
	}
	resolve := func(key string) (string, bool) {
		v, ok := config.Volumes[key]
		if !ok || isExternalVolume(v.External) {
			return "", false
		}
		name := v.Name
		if name == "" {
			name = project + "_" + key
		}
		return name, !externalNames[name] && dockerVolumeNamePattern.MatchString(name)
	}
	used := map[string]map[string]bool{}
	for owner, svc := range config.Services {
		for _, mount := range svc.Volumes {
			if mount.Type != "volume" {
				continue
			}
			// External aliases also count as references to a Docker volume.
			v, ok := config.Volumes[mount.Source]
			if !ok {
				continue
			}
			name := v.Name
			if name == "" {
				name = project + "_" + mount.Source
			}
			if used[name] == nil {
				used[name] = map[string]bool{}
			}
			used[name][owner] = true
		}
	}
	seen := map[string]bool{}
	for _, mount := range config.Services[service].Volumes {
		if mount.Type != "volume" {
			continue
		}
		name, ok := resolve(mount.Source)
		if ok && len(used[name]) == 1 {
			seen[name] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// ListServiceDeletionVolumes resolves Compose interpolation without exposing
// configuration or environment values to the caller.
func (r CommandRunner) ListServiceDeletionVolumes(ctx context.Context, directory, service string) ([]application.ServiceDeletionVolume, error) {
	ctx = normalizeContext(ctx)
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	file, err := findComposeFile(directory)
	if err != nil {
		return nil, err
	}
	output, err := runStructuredCommand(composeCommand(ctx, binary, directory, file, "config", "--format", "json"), "resolve service volumes", directory)
	if err != nil {
		return nil, errors.New("could not resolve service volumes")
	}
	project := composeProjectName(directory)
	names, err := serviceVolumeCandidates(output, project, service)
	if err != nil {
		return nil, err
	}
	volumes := make([]application.ServiceDeletionVolume, 0, len(names))
	for _, name := range names {
		exists, err := dockerVolumeExists(ctx, binary, directory, name)
		if err != nil {
			return nil, err
		}
		if exists {
			labels, err := dockerVolumeLabels(ctx, binary, directory, name)
			if err != nil {
				return nil, err
			}
			if labels["com.docker.compose.project"] != project {
				continue
			}
		}
		// Missing volumes remain eligible so an interrupted removal is idempotent.
		volumes = append(volumes, application.ServiceDeletionVolume{Name: name})
	}
	return volumes, nil
}

// RemoveServiceVolumes revalidates configuration and ownership immediately
// before removing volumes. Docker's non-forced removal rejects in-use volumes.
func (r CommandRunner) RemoveServiceVolumes(ctx context.Context, directory, service string, names []string) error {
	ctx = normalizeContext(ctx)
	volumes, err := r.ListServiceDeletionVolumes(ctx, directory, service)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, v := range volumes {
		allowed[v.Name] = true
	}
	for _, name := range names {
		if !allowed[name] {
			return errors.New("selected volume is not eligible for deletion")
		}
	}
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	for _, name := range names {
		exists, err := dockerVolumeExists(ctx, binary, directory, name)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		labels, err := dockerVolumeLabels(ctx, binary, directory, name)
		if err != nil {
			return err
		}
		if labels["com.docker.compose.project"] != composeProjectName(directory) {
			return errors.New("volume ownership changed")
		}
		command := exec.CommandContext(ctx, binary, "volume", "rm", name)
		command.Dir = directory
		command.Env = composeProcessEnvironment(os.Environ(), nil)
		if _, err := runStructuredCommand(command, "remove service volume", directory); err != nil {
			return fmt.Errorf("remove service volume: %w", err)
		}
	}
	return nil
}
