package version

import "testing"

func TestStableVersionDetection(t *testing.T) {
	original := Version
	defer func() { Version = original }()
	for _, testCase := range []struct {
		version string
		dev     bool
	}{
		{"dev", true},
		{"v1.2.3", false},
		{"v1.2", true},
		{"v1.2.3-dirty", true},
		{"1.2.3", true},
		{"v0.0.0", false},
	} {
		Version = testCase.version
		if got := IsDevelopment(); got != testCase.dev {
			t.Fatalf("IsDevelopment(%q) = %t, want %t", testCase.version, got, testCase.dev)
		}
	}
}

func TestCurrentReportsDeploymentContract(t *testing.T) {
	info := Current()
	if info.DeploymentContract != DeploymentContract {
		t.Fatalf("deployment contract = %d, want %d", info.DeploymentContract, DeploymentContract)
	}
}
