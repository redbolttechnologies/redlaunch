package systemd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerInstallsAndDisablesUnitPair(t *testing.T) {
	root := t.TempDir()
	unitDirectory := filepath.Join(root, "units")
	if err := os.Mkdir(unitDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "systemctl")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"${0%/*}/calls\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(unitDirectory, binary)
	if err != nil {
		t.Fatal(err)
	}
	serviceName := "redlaunch-backup-a7-s11.service"
	timerName := "redlaunch-backup-a7-s11.timer"
	if err := manager.Install(context.Background(), serviceName, "[Service]\nType=oneshot\n", timerName, "[Timer]\nOnCalendar=*-*-* 03:00:00\n"); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name    string
		content string
	}{
		{name: serviceName, content: "[Service]\nType=oneshot\n"},
		{name: timerName, content: "[Timer]\nOnCalendar=*-*-* 03:00:00\n"},
	} {
		got, err := os.ReadFile(filepath.Join(unitDirectory, testCase.name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != testCase.content {
			t.Fatalf("unit %s = %q, want %q", testCase.name, got, testCase.content)
		}
	}
	if err := manager.Disable(context.Background(), serviceName, timerName); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{serviceName, timerName} {
		if _, err := os.Stat(filepath.Join(unitDirectory, name)); !os.IsNotExist(err) {
			t.Fatalf("unit %s stat error = %v, want not exist", name, err)
		}
	}
	calls, err := os.ReadFile(filepath.Join(root, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "daemon-reload") || !strings.Contains(string(calls), "enable") || !strings.Contains(string(calls), "disable") || !strings.Contains(string(calls), "stop") {
		t.Fatalf("systemctl calls = %q, want reload, enable, disable, and stop", calls)
	}
}

func TestManagerRejectsUnsafeUnitNames(t *testing.T) {
	manager, err := NewManager(t.TempDir(), "systemctl")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Install(context.Background(), "../backup.service", "", "backup.timer", ""); err == nil {
		t.Fatal("Install() error = nil, want unsafe unit name rejection")
	}
}

func TestManagerUsesDBusControlBinary(t *testing.T) {
	root := t.TempDir()
	unitDirectory := filepath.Join(root, "units")
	if err := os.Mkdir(unitDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "dbus-send")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"${0%/*}/calls\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(unitDirectory, binary)
	if err != nil {
		t.Fatal(err)
	}
	serviceName := "redlaunch-backup-a7-s11.service"
	timerName := "redlaunch-backup-a7-s11.timer"
	if err := manager.Install(context.Background(), serviceName, "", timerName, ""); err != nil {
		t.Fatal(err)
	}
	if err := manager.Disable(context.Background(), serviceName, timerName); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(filepath.Join(root, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"org.freedesktop.systemd1.Manager.Reload",
		"org.freedesktop.systemd1.Manager.EnableUnitFiles",
		"array:string:" + timerName,
		"org.freedesktop.systemd1.Manager.StartUnit",
		"org.freedesktop.systemd1.Manager.DisableUnitFiles",
		"org.freedesktop.systemd1.Manager.StopUnit",
	} {
		if !strings.Contains(string(calls), expected) {
			t.Fatalf("D-Bus calls = %q, want %q", calls, expected)
		}
	}
}

func TestManagerUsesUserSystemdScope(t *testing.T) {
	root := t.TempDir()
	unitDirectory := filepath.Join(root, "units")
	if err := os.Mkdir(unitDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "systemctl")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$(dirname \"$0\")/calls\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManagerWithScope(unitDirectory, binary, ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Install(context.Background(), "redlaunch-backup-a7-s11.service", "", "redlaunch-backup-a7-s11.timer", ""); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(filepath.Join(root, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "--user") {
		t.Fatalf("systemctl calls = %q, want --user", calls)
	}
}

func TestManagerUsesUserSessionBus(t *testing.T) {
	root := t.TempDir()
	unitDirectory := filepath.Join(root, "units")
	if err := os.Mkdir(unitDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "dbus-send")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$(dirname \"$0\")/calls\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManagerWithScope(unitDirectory, binary, ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Install(context.Background(), "redlaunch-backup-a7-s11.service", "", "redlaunch-backup-a7-s11.timer", ""); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(filepath.Join(root, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "--session") {
		t.Fatalf("D-Bus calls = %q, want --session", calls)
	}
}

func TestManagerFailsWithoutWritingUnitsWhenControllerIsMissing(t *testing.T) {
	root := t.TempDir()
	unitDirectory := filepath.Join(root, "units")
	missing := filepath.Join(root, "missing-systemctl")
	manager, err := NewManager(unitDirectory, missing)
	if err != nil {
		t.Fatal(err)
	}
	serviceName := "redlaunch-backup-a7-s11.service"
	timerName := "redlaunch-backup-a7-s11.timer"
	err = manager.Install(context.Background(), serviceName, "[Service]\n", timerName, "[Timer]\n")
	if err == nil || !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("Install(missing controller) error = %v, want %v", err, exec.ErrNotFound)
	}
	for _, name := range []string{serviceName, timerName} {
		if _, statErr := os.Stat(filepath.Join(unitDirectory, name)); !os.IsNotExist(statErr) {
			t.Fatalf("unit %s stat error = %v, want not exist after missing controller", name, statErr)
		}
	}
	if err := manager.Disable(context.Background(), serviceName, timerName); err == nil || !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("Disable(missing controller) error = %v, want %v", err, exec.ErrNotFound)
	}
}
