package compose

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRegistryMaintenanceLifecycleIntegration(t *testing.T) {
	if os.Getenv("REDLAUNCH_REGISTRY_INTEGRATION") != "1" {
		t.Skip("set REDLAUNCH_REGISTRY_INTEGRATION=1 to run disposable registry checks")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not available")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	name := "redlaunch-registry-test-" + suffix
	volume := name + "-data"
	dropImage := "127.0.0.1:" + strconv.Itoa(port) + "/redlaunch-drop:v1"
	keepImage := "127.0.0.1:" + strconv.Itoa(port) + "/redlaunch-keep:v1"

	runDocker := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(ctx, "docker", args...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, output)
		}
		return string(output)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
		_ = exec.Command("docker", "volume", "rm", volume).Run()
		_ = exec.Command("docker", "image", "rm", "-f", dropImage, keepImage).Run()
	})

	runDocker("run", "-d", "--name", name, "-p", "127.0.0.1:"+strconv.Itoa(port)+":5000", "-v", volume+":/var/lib/registry", "registry:3.1.1")
	waitHTTP(t, "http://127.0.0.1:"+strconv.Itoa(port)+"/v2/")
	runDocker("tag", "registry:3.1.1", dropImage)
	runDocker("push", dropImage)
	runDocker("pull", "alpine:latest")
	runDocker("tag", "alpine:latest", keepImage)
	runDocker("push", keepImage)

	runner := CommandRunner{}
	target, err := runner.RegistryContainerTarget(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := RegistryReadOnlyConfig(target.Config)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyRegistryConfig(ctx, name, target.ConfigPath, readOnly); err != nil {
		t.Fatal(err)
	}
	if err := runner.RestartRegistryContainer(ctx, name); err != nil {
		t.Fatal(err)
	}
	if err := runner.WaitRegistryReady(ctx, name, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	accepted, err := runner.RegistryWritesAccepted(ctx, name, "redlaunch-maintenance-write-probe")
	if err != nil {
		t.Fatal(err)
	}
	if accepted {
		t.Fatal("registry writes were accepted in read-only mode")
	}

	contents, err := runner.ListRegistryContents(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	var digest string
	for _, content := range contents {
		if content.Repository == "redlaunch-drop" && content.Tag == "v1" {
			digest = content.Digest
			break
		}
	}
	if digest == "" {
		t.Fatalf("pushed digest not found in %#v", contents)
	}
	before, beforeKnown := runner.RegistryStorageBytes(ctx, name)
	if err := runner.DeleteRegistryVersion(ctx, name, "redlaunch-drop", digest); err != nil {
		t.Fatal(err)
	}
	if err := runner.GarbageCollectRegistry(ctx, name, target.ConfigPath); err != nil {
		t.Fatal(err)
	}
	after, afterKnown := runner.RegistryStorageBytes(ctx, name)
	if !beforeKnown || !afterKnown || after >= before {
		t.Fatalf("storage before=%d known=%v after=%d known=%v, want a reduction", before, beforeKnown, after, afterKnown)
	}

	if err := runner.ApplyRegistryConfig(ctx, name, target.ConfigPath, target.Config); err != nil {
		t.Fatal(err)
	}
	if err := runner.RestartRegistryContainer(ctx, name); err != nil {
		t.Fatal(err)
	}
	if err := runner.WaitRegistryReady(ctx, name, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	accepted, err = runner.RegistryWritesAccepted(ctx, name, "redlaunch-maintenance-write-probe")
	if err != nil {
		t.Fatal(err)
	}
	if !accepted {
		t.Fatal("registry did not return to writable mode")
	}
	if err := runner.RemoveRegistryRepository(ctx, name, "redlaunch-maintenance-write-probe"); err != nil {
		t.Fatal(err)
	}
	runDocker("pull", keepImage)
}

func waitHTTP(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(url)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("registry at %s did not become ready", url)
}
