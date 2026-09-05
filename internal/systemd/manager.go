// Package systemd manages the isolated units used by Redlaunch schedules.
package systemd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const maxCommandOutput = 8 * 1024

const (
	systemdBusDestination = "org.freedesktop.systemd1"
	systemdBusObject      = "/org/freedesktop/systemd1"
	systemdBusInterface   = "org.freedesktop.systemd1.Manager"
	ScopeSystem           = "system"
	ScopeUser             = "user"
)

// Manager writes system units and asks systemd to load or stop them. Unit
// names are supplied by the service layer and contain only stable numeric
// resource IDs, which keeps resources from different applications isolated.
type Manager struct {
	UnitDirectory string
	Binary        string
	Scope         string
}

// NewManager creates a systemd manager for one unit directory.
func NewManager(unitDirectory, binary string) (Manager, error) {
	return NewManagerWithScope(unitDirectory, binary, ScopeSystem)
}

// NewManagerWithScope creates a systemd manager for either the system or the
// current user's unit directory.
func NewManagerWithScope(unitDirectory, binary, scope string) (Manager, error) {
	if strings.TrimSpace(unitDirectory) == "" {
		return Manager{}, errors.New("systemd unit directory is required")
	}
	unitDirectory, err := filepath.Abs(unitDirectory)
	if err != nil {
		return Manager{}, fmt.Errorf("resolve systemd unit directory: %w", err)
	}
	unitDirectory = filepath.Clean(unitDirectory)
	if unitDirectory == string(filepath.Separator) {
		return Manager{}, errors.New("systemd unit directory must not be the filesystem root")
	}
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope != ScopeSystem && scope != ScopeUser {
		return Manager{}, fmt.Errorf("unsupported systemd scope %q", scope)
	}
	if binary == "" {
		binary = "systemctl"
	}
	return Manager{UnitDirectory: unitDirectory, Binary: binary, Scope: scope}, nil
}

// Install writes a service and its timer, reloads systemd, and enables the
// timer immediately.
func (m Manager) Install(ctx context.Context, serviceUnitName, serviceContents, timerUnitName, timerContents string) error {
	if err := validateUnitName(serviceUnitName); err != nil {
		return err
	}
	if err := validateUnitName(timerUnitName); err != nil {
		return err
	}
	if err := ensureUnitDirectory(m.UnitDirectory); err != nil {
		return fmt.Errorf("ensure systemd unit directory: %w", err)
	}
	if err := writeUnitFile(filepath.Join(m.UnitDirectory, serviceUnitName), serviceContents); err != nil {
		return fmt.Errorf("write systemd service unit: %w", err)
	}
	if err := writeUnitFile(filepath.Join(m.UnitDirectory, timerUnitName), timerContents); err != nil {
		return fmt.Errorf("write systemd timer unit: %w", err)
	}
	if err := m.run(ctx, "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd units: %w", err)
	}
	if err := m.run(ctx, "enable", "--now", timerUnitName); err != nil {
		return fmt.Errorf("enable systemd timer: %w", err)
	}
	return nil
}

// Disable stops and removes a timer and its one-shot service.
func (m Manager) Disable(ctx context.Context, serviceUnitName, timerUnitName string) error {
	if err := validateUnitName(serviceUnitName); err != nil {
		return err
	}
	if err := validateUnitName(timerUnitName); err != nil {
		return err
	}
	if err := m.run(ctx, "disable", "--now", timerUnitName); err != nil {
		return fmt.Errorf("disable systemd timer: %w", err)
	}
	if err := m.run(ctx, "stop", serviceUnitName); err != nil {
		return fmt.Errorf("stop systemd backup service: %w", err)
	}
	if err := removeUnitFile(filepath.Join(m.UnitDirectory, timerUnitName)); err != nil {
		return fmt.Errorf("remove systemd timer unit: %w", err)
	}
	if err := removeUnitFile(filepath.Join(m.UnitDirectory, serviceUnitName)); err != nil {
		return fmt.Errorf("remove systemd service unit: %w", err)
	}
	if err := m.run(ctx, "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd units after disable: %w", err)
	}
	return nil
}

func (m Manager) run(ctx context.Context, args ...string) error {
	binary := m.Binary
	if binary == "" {
		binary = "systemctl"
	}
	if filepath.Base(binary) == "dbus-send" {
		return m.runDBus(ctx, binary, args...)
	}
	commandArgs := append([]string{m.systemdScopeArgument()}, args...)
	command := exec.CommandContext(ctx, binary, commandArgs...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		details := strings.TrimSpace(stderr.String())
		if len(details) > maxCommandOutput {
			details = "..." + details[len(details)-maxCommandOutput:]
		}
		if details != "" {
			return fmt.Errorf("%w: %s", err, details)
		}
		return err
	}
	return nil
}

func (m Manager) runDBus(ctx context.Context, binary string, args ...string) error {
	switch {
	case len(args) == 1 && args[0] == "daemon-reload":
		return m.runDBusMethod(ctx, binary, "Reload")
	case len(args) == 3 && args[0] == "enable" && args[1] == "--now":
		unitName := args[2]
		if err := m.runDBusMethod(ctx, binary, "EnableUnitFiles", "array:string:"+unitName, "boolean:false", "boolean:false"); err != nil {
			return err
		}
		return m.runDBusMethod(ctx, binary, "StartUnit", "string:"+unitName, "string:replace")
	case len(args) == 3 && args[0] == "disable" && args[1] == "--now":
		unitName := args[2]
		if err := m.runDBusMethod(ctx, binary, "DisableUnitFiles", "array:string:"+unitName, "boolean:false"); err != nil {
			return err
		}
		return m.runDBusMethod(ctx, binary, "StopUnit", "string:"+unitName, "string:replace")
	case len(args) == 2 && args[0] == "stop":
		return m.runDBusMethod(ctx, binary, "StopUnit", "string:"+args[1], "string:replace")
	default:
		return fmt.Errorf("unsupported systemd D-Bus operation %q", args)
	}
}

func (m Manager) runDBusMethod(ctx context.Context, binary, method string, values ...string) error {
	commandArgs := []string{
		m.dbusBusArgument(),
		"--print-reply=literal",
		"--dest=" + systemdBusDestination,
		systemdBusObject,
		systemdBusInterface + "." + method,
	}
	commandArgs = append(commandArgs, values...)
	command := exec.CommandContext(ctx, binary, commandArgs...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return commandError(err, output.Bytes())
	}
	return nil
}

func (m Manager) systemdScopeArgument() string {
	if m.Scope == ScopeUser {
		return "--user"
	}
	return "--system"
}

func (m Manager) dbusBusArgument() string {
	if m.Scope == ScopeUser {
		return "--session"
	}
	return "--system"
}

func commandError(err error, output []byte) error {
	details := strings.TrimSpace(string(output))
	if len(details) > maxCommandOutput {
		details = "..." + details[len(details)-maxCommandOutput:]
	}
	if details != "" {
		return fmt.Errorf("%w: %s", err, details)
	}
	return err
}

func ensureUnitDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return err
		}
		info, err = os.Lstat(directory)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("systemd unit directory must be a directory")
	}
	return nil
}

func writeUnitFile(path, contents string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("systemd unit path must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".redlaunch-systemd-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}

func removeUnitFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("systemd unit path must be a regular file")
	}
	return os.Remove(path)
}

func validateUnitName(name string) error {
	if name == "" {
		return errors.New("systemd unit name is required")
	}
	for _, character := range name {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			continue
		}
		return fmt.Errorf("invalid systemd unit name %q", name)
	}
	return nil
}
