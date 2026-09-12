package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Data-directory contract.
//
// Managed workloads live under a projects root with two fixed subtrees:
// "applications/<name>/" and "core/<component>/". Redlaunch owns the project
// directory itself, compose.yml, vars.env, secrets.env, and generated
// configuration (Caddyfile, gateway files, scoped database environment
// files). A managed container must never be given a bind mount that resolves
// to the project directory itself: that would let the workload read or
// replace sibling managed files such as secrets.env. Bind sources must name a
// file or subdirectory below the project root, or a named volume.
//
// Compose filename convention.
//
// New managed projects always write compose.yml. Both compose.yml and
// compose.yaml are accepted when reading so older installations keep working;
// when both exist, compose.yml wins.
const preferredComposeFileName = "compose.yml"

func supportedComposeFileNames() []string {
	return []string{"compose.yml", "compose.yaml"}
}

// checkManagedAncestors rejects symlink escapes on every existing ancestor
// from root down to (but excluding) candidate itself. Callers still validate
// the final component with Lstat. Lexical containment alone is not enough:
// an ancestor replaced by a symlink after directory creation would otherwise
// redirect all subsequent file access outside the managed tree.
func checkManagedAncestors(root, candidate string) error {
	cleanRoot := filepath.Clean(root)
	relative, err := filepath.Rel(cleanRoot, filepath.Clean(candidate))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("path is outside the managed directory")
	}
	if relative == "." {
		return nil
	}
	current := cleanRoot
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "." || component == "" {
			continue
		}
		current = filepath.Join(current, component)
		// Stop before the final component; the caller inspects it.
		if current == filepath.Clean(candidate) {
			break
		}
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed path ancestor must not be a symlink")
		}
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil {
			return err
		}
		resolvedRelative, err := filepath.Rel(cleanRoot, resolved)
		if err != nil || resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) {
			return errors.New("managed path ancestor resolves outside the managed directory")
		}
	}
	return nil
}

// checkResolvedDirectoryContainment resolves both root and directory through
// any symlinks and requires the resolved directory to remain inside the
// resolved root. Lstat on the final component alone cannot see a symlinked
// ancestor, while lexical containment cannot see where that ancestor points.
func checkResolvedDirectoryContainment(root, directory string) error {
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return err
	}
	resolvedDirectory, err := filepath.EvalSymlinks(filepath.Clean(directory))
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedDirectory)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("managed directory resolves outside the managed directory")
	}
	return nil
}

// isProjectRootBind reports whether a bind source resolves to the project
// directory itself. Such a mount would expose every managed file of the
// project (including secrets.env) to the workload and must be rejected.
func isProjectRootBind(root, raw string) bool {
	value := strings.TrimSpace(strings.Trim(raw, "\"'"))
	if separator := strings.IndexByte(value, ':'); separator >= 0 {
		value = value[:separator]
	}
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == "./" {
		return true
	}
	cleaned := filepath.Clean(value)
	if cleaned == "." {
		return true
	}
	candidate := filepath.Join(root, cleaned)
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "."
}
