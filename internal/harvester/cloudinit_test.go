package harvester

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/bk201-org/harvester-scaleset/internal/release"
)

func TestRenderCloudInit(t *testing.T) {
	t.Parallel()
	data, err := RenderCloudInit(BootstrapConfig{
		RunnerUser: "runner",
		RunnerHome: "/opt/actions-runner",
		JITConfig:  "encoded-jit",
		Artifact: release.Artifact{
			Version: "2.999.0",
			URL:     "https://example.test/actions-runner.tar.gz",
			SHA256:  "abc123",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "#cloud-config\n") {
		t.Fatalf("missing cloud-config header: %s", text)
	}
	encodedJIT := base64.StdEncoding.EncodeToString([]byte("ACTIONS_RUNNER_INPUT_JITCONFIG=encoded-jit\n"))
	if !strings.Contains(text, encodedJIT) {
		t.Fatal("cloud-config does not contain the base64-encoded JIT environment file")
	}
	if strings.Contains(text, "ACTIONS_RUNNER_INPUT_JITCONFIG=encoded-jit") {
		t.Fatal("JIT configuration was emitted in plaintext")
	}
}

func TestRenderCloudInitRejectsIncompleteInput(t *testing.T) {
	t.Parallel()
	if _, err := RenderCloudInit(BootstrapConfig{}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestRenderCloudInitForPreinstalledRunner(t *testing.T) {
	t.Parallel()
	data, err := RenderCloudInit(BootstrapConfig{
		RunnerUser:        "runner",
		RunnerHome:        "/opt/actions-runner",
		JITConfig:         "encoded-jit",
		SkipRunnerInstall: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "/usr/local/sbin/install-actions-runner") {
		t.Fatal("preinstalled-runner cloud-config includes the runner installer")
	}
	if !strings.Contains(text, "/opt/actions-runner/run.sh") {
		t.Fatal("preinstalled-runner cloud-config does not verify the baked runner")
	}
	if !strings.Contains(text, "github-actions-runner.service") {
		t.Fatal("preinstalled-runner cloud-config does not start the runner service")
	}
}

func TestRenderCloudInitRequiresArtifactWhenInstalling(t *testing.T) {
	t.Parallel()
	_, err := RenderCloudInit(BootstrapConfig{
		RunnerUser: "runner",
		RunnerHome: "/opt/actions-runner",
		JITConfig:  "encoded-jit",
	})
	if err == nil || !strings.Contains(err.Error(), "artifact") {
		t.Fatalf("expected artifact validation error, got %v", err)
	}
}
