package harvester

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/bk201-org/harvester-scaleset/internal/config"
	"github.com/bk201-org/harvester-scaleset/internal/release"
	"sigs.k8s.io/yaml"
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

func TestRenderCloudInitWithSSHKey(t *testing.T) {
	t.Parallel()
	const publicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGV4YW1wbGU= example@example.test"
	data, err := RenderCloudInit(BootstrapConfig{
		RunnerUser:        "runner",
		RunnerHome:        "/opt/actions-runner",
		JITConfig:         "encoded-jit",
		SSHUsername:       "ubuntu",
		SSHPublicKey:      publicKey,
		SkipRunnerInstall: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Users []any `json:"users"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Users) != 2 || config.Users[0] != "default" {
		t.Fatalf("cloud-init users = %#v", config.Users)
	}
	user, ok := config.Users[1].(map[string]any)
	if !ok || user["name"] != "ubuntu" {
		t.Fatalf("SSH user = %#v", config.Users[1])
	}
	keys, ok := user["ssh_authorized_keys"].([]any)
	if !ok || len(keys) != 1 || keys[0] != publicKey {
		t.Fatalf("SSH authorized keys = %#v", user["ssh_authorized_keys"])
	}
}

func TestRenderCloudInitRunnerSSHKeyPreservesHome(t *testing.T) {
	t.Parallel()
	data, err := RenderCloudInit(BootstrapConfig{
		RunnerUser: "runner", RunnerHome: "/opt/actions-runner", JITConfig: "encoded-jit",
		SSHUsername: "runner", SSHPublicKey: "ssh-ed25519 AAAA example",
		Artifact: release.Artifact{URL: "https://example.test/runner.tar.gz", SHA256: "abc123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Users []any `json:"users"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	user := config.Users[1].(map[string]any)
	if user["homedir"] != "/opt/actions-runner" || user["shell"] != "/bin/bash" {
		t.Fatalf("runner account settings = %#v", user)
	}
}

func TestRenderCloudInitRejectsRootSSHUser(t *testing.T) {
	t.Parallel()
	_, err := RenderCloudInit(BootstrapConfig{
		RunnerUser: "runner", RunnerHome: "/opt/actions-runner", JITConfig: "encoded-jit",
		SSHUsername: "root", SSHPublicKey: "ssh-ed25519 AAAA example", SkipRunnerInstall: true,
	})
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("expected root SSH username error, got %v", err)
	}
}

func TestRenderCloudInitWithHostsAndCACerts(t *testing.T) {
	t.Parallel()
	for _, skipInstall := range []bool{false, true} {
		t.Run(fmt.Sprintf("skip-install=%t", skipInstall), func(t *testing.T) {
			data, err := RenderCloudInit(BootstrapConfig{
				RunnerUser: "runner", RunnerHome: "/opt/actions-runner", JITConfig: "encoded-jit",
				SkipRunnerInstall: skipInstall,
				Artifact:          release.Artifact{URL: "https://example.test/runner.tar.gz", SHA256: "abc123"},
				HostMappings: []config.HostMapping{
					{Host: "git.internal", IP: "192.0.2.20"},
					{Host: "v6.internal", IP: "2001:db8::20"},
				},
				TrustedCACerts: []string{"cert-one", "cert-two"},
			})
			if err != nil {
				t.Fatal(err)
			}
			var cloud cloudConfig
			if err := yaml.Unmarshal(data, &cloud); err != nil {
				t.Fatal(err)
			}
			if cloud.ManageEtcHosts == nil || *cloud.ManageEtcHosts {
				t.Fatalf("manage_etc_hosts = %v, want false", cloud.ManageEtcHosts)
			}
			if cloud.CACerts == nil || !reflect.DeepEqual(cloud.CACerts.Trusted, []string{"cert-one", "cert-two"}) {
				t.Fatalf("ca_certs = %#v", cloud.CACerts)
			}
			var hostsFiles []cloudFile
			for _, file := range cloud.WriteFiles {
				if file.Path == "/etc/hosts" {
					hostsFiles = append(hostsFiles, file)
				}
			}
			if len(hostsFiles) != 1 || !hostsFiles[0].Append ||
				hostsFiles[0].Content != "\n192.0.2.20 git.internal\n2001:db8::20 v6.internal\n" {
				t.Fatalf("hosts write_files = %#v", hostsFiles)
			}
		})
	}
}

func TestRenderCloudInitWithoutExtrasLeavesModulesUnset(t *testing.T) {
	t.Parallel()
	data, err := RenderCloudInit(BootstrapConfig{
		RunnerUser: "runner", RunnerHome: "/opt/actions-runner", JITConfig: "encoded-jit",
		SkipRunnerInstall: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var cloud cloudConfig
	if err := yaml.Unmarshal(data, &cloud); err != nil {
		t.Fatal(err)
	}
	if cloud.ManageEtcHosts != nil || cloud.CACerts != nil {
		t.Fatalf("unexpected optional modules: hosts=%v ca=%v", cloud.ManageEtcHosts, cloud.CACerts)
	}
	for _, file := range cloud.WriteFiles {
		if file.Path == "/etc/hosts" {
			t.Fatal("wrote /etc/hosts without host mappings")
		}
	}
}
