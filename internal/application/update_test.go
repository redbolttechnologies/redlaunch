package application

import "testing"

func TestParseReleaseVersionAndComparison(t *testing.T) {
	valid := map[string][3]int{
		"v0.0.0":   {0, 0, 0},
		"v1.2.3":   {1, 2, 3},
		"v10.0.12": {10, 0, 12},
	}
	for value, want := range valid {
		got, err := ParseReleaseVersion(value)
		if err != nil || got != want {
			t.Fatalf("ParseReleaseVersion(%q) = %#v, %v; want %#v", value, got, err, want)
		}
	}
	for _, value := range []string{"", "1.2.3", "v1.2", "v1.2.3-rc1", "v1.2.x"} {
		if _, err := ParseReleaseVersion(value); err == nil {
			t.Fatalf("ParseReleaseVersion(%q) succeeded, want error", value)
		}
	}
	newer, err := IsNewerRelease("v1.2.3", "v1.3.0")
	if err != nil || !newer {
		t.Fatalf("IsNewerRelease = %t, %v; want true", newer, err)
	}
	newer, err = IsNewerRelease("v1.3.0", "v1.2.9")
	if err != nil || newer {
		t.Fatalf("IsNewerRelease downgrade = %t, %v; want false", newer, err)
	}
	if _, err := IsNewerRelease("dev", "v1.0.0"); err == nil {
		t.Fatal("IsNewerRelease(dev) succeeded, want error")
	}
}
