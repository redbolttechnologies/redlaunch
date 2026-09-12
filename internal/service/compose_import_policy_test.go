package service

import (
	"strings"
	"testing"
)

// TestValidateImportedComposePolicyRejectsForbiddenReferences covers the R01
// synthetic bypasses: file-backed labels, flow-mapping binds, and explicit
// volume names must fail closed before Compose is invoked.
func TestValidateImportedComposePolicyRejectsForbiddenReferences(t *testing.T) {
	root := t.TempDir()
	for name, contents := range map[string]string{
		"label_file": `services:
  web:
    image: busybox:1.36
    label_file: /absolute/path/to/a/temporary-review-labels.txt
`,
		"flow_bind": `services:
  web:
    image: busybox:1.36
    volumes: [{type: bind, source: /tmp/redlaunch-review-synthetic, target: /data}]
`,
		"flow_bind_block": `services:
  web:
    image: busybox:1.36
    volumes:
      - {type: bind, source: /tmp/redlaunch-review-synthetic, target: /data}
`,
		"explicit_volume_name": `services:
  web:
    image: busybox:1.36
    volumes: [reviewdata:/data]
volumes:
  reviewdata: {name: redlaunch-review-foreign}
`,
		"explicit_network_name": `services:
  web:
    image: busybox:1.36
networks:
  frontend: {name: redlaunch-review-foreign-net}
`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateImportedComposePolicy(contents, root); err == nil {
				t.Fatalf("validateImportedComposePolicy(%s) = nil, want rejection", name)
			}
		})
	}
}

// TestValidateImportedComposePolicyAllowsManagedSubset guards against
// over-blocking: short-syntax subdirectory binds, named volumes, and empty
// mappings remain accepted.
func TestValidateImportedComposePolicyAllowsManagedSubset(t *testing.T) {
	root := t.TempDir()
	contents := `services:
  web:
    image: busybox:1.36
    volumes:
      - ./data:/data
      - reviewdata:/data
volumes:
  reviewdata:
`
	if err := validateImportedComposePolicy(contents, root); err != nil {
		t.Fatalf("validateImportedComposePolicy(managed subset) = %v, want nil", err)
	}
	if err := validateImportedVolumeSource(root, "./data:/data"); err != nil {
		t.Fatalf("validateImportedVolumeSource(subdirectory) = %v, want nil", err)
	}
	for _, flow := range []string{"{type: bind}", "{name: foreign}"} {
		if !isFlowMapping(flow) {
			t.Fatalf("isFlowMapping(%q) = false, want true", flow)
		}
	}
	if isFlowMapping("[reviewdata:/data]") {
		t.Fatal("isFlowMapping(inline list) = true, want false (lists are split into entries)")
	}
	if isNonEmptyFlowMapping("{}") {
		t.Fatal("isNonEmptyFlowMapping({}) = true, want false")
	}
	if strings.Contains("no braces here", "{") {
		t.Fatal("unreachable")
	}
}
