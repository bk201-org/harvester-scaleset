package harvester

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bk201-org/harvester-scaleset/internal/release"
	"sigs.k8s.io/yaml"
)

type cloudConfig struct {
	PackageUpdate bool        `json:"package_update"`
	WriteFiles    []cloudFile `json:"write_files"`
	RunCmd        [][]string  `json:"runcmd"`
	Users         []any       `json:"users,omitempty"`
}

type cloudFile struct {
	Path        string `json:"path"`
	Owner       string `json:"owner,omitempty"`
	Permissions string `json:"permissions"`
	Encoding    string `json:"encoding,omitempty"`
	Content     string `json:"content"`
}

type cloudUser struct {
	Name              string   `json:"name"`
	HomeDir           string   `json:"homedir,omitempty"`
	Shell             string   `json:"shell,omitempty"`
	SSHAuthorizedKeys []string `json:"ssh_authorized_keys"`
}

type BootstrapConfig struct {
	RunnerUser        string
	SSHUsername       string
	SSHPublicKey      string
	RunnerHome        string
	JITConfig         string
	Artifact          release.Artifact
	SkipRunnerInstall bool
}

func RenderCloudInit(c BootstrapConfig) ([]byte, error) {
	if c.RunnerUser == "" || c.RunnerHome == "" || c.JITConfig == "" {
		return nil, fmt.Errorf("cloud-init bootstrap configuration is incomplete")
	}
	if c.SSHPublicKey != "" && c.SSHUsername == "" {
		return nil, fmt.Errorf("cloud-init SSH username is required with a public key")
	}
	if c.SSHPublicKey != "" && c.SSHUsername == "root" {
		return nil, fmt.Errorf("cloud-init SSH username cannot be root")
	}
	if err := validateGuestPath(c.RunnerHome); err != nil {
		return nil, err
	}
	if !c.SkipRunnerInstall && (c.Artifact.URL == "" || c.Artifact.SHA256 == "") {
		return nil, fmt.Errorf("cloud-init runner artifact is incomplete")
	}

	unit := fmt.Sprintf(`[Unit]
Description=Ephemeral GitHub Actions Runner
After=network-online.target cloud-init.service
Wants=network-online.target

[Service]
Type=simple
User=%s
WorkingDirectory=%s
EnvironmentFile=/etc/actions-runner/jit.env
ExecStart=%s/run.sh
ExecStopPost=/usr/bin/rm -f /etc/actions-runner/jit.env
Restart=no

[Install]
WantedBy=multi-user.target
`, c.RunnerUser, c.RunnerHome, c.RunnerHome)

	config := cloudConfig{
		PackageUpdate: false,
		WriteFiles: []cloudFile{
			{
				Path:        "/etc/systemd/system/github-actions-runner.service",
				Owner:       "root:root",
				Permissions: "0644",
				Encoding:    "b64",
				Content:     base64.StdEncoding.EncodeToString([]byte(unit)),
			},
			{
				Path:        "/etc/actions-runner/jit.env",
				Owner:       "root:root",
				Permissions: "0600",
				Encoding:    "b64",
				Content:     base64.StdEncoding.EncodeToString([]byte("ACTIONS_RUNNER_INPUT_JITCONFIG=" + c.JITConfig + "\n")),
			},
		},
	}
	if c.SSHPublicKey != "" {
		sshUser := cloudUser{Name: c.SSHUsername, SSHAuthorizedKeys: []string{c.SSHPublicKey}}
		if c.SSHUsername == c.RunnerUser {
			sshUser.HomeDir = c.RunnerHome
			sshUser.Shell = "/bin/bash"
		}
		config.Users = []any{"default", sshUser}
	}
	if c.SkipRunnerInstall {
		config.RunCmd = [][]string{
			{"/usr/bin/id", "--user", c.RunnerUser},
			{"/usr/bin/test", "-x", c.RunnerHome + "/run.sh"},
			{"systemctl", "daemon-reload"},
			{"systemctl", "enable", "--now", "github-actions-runner.service"},
		}
		return marshalCloudConfig(config)
	}

	installScript := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

runner_user=%q
runner_home=%q
archive=/tmp/actions-runner.tar.gz

if command -v apt-get >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y --no-install-recommends ca-certificates curl tar gzip
elif command -v zypper >/dev/null 2>&1; then
  zypper --non-interactive install ca-certificates curl tar gzip
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y ca-certificates curl tar gzip
elif command -v yum >/dev/null 2>&1; then
  yum install -y ca-certificates curl tar gzip
else
  echo "unsupported image: apt-get, zypper, dnf, or yum is required" >&2
  exit 1
fi

if ! id "${runner_user}" >/dev/null 2>&1; then
  useradd --create-home --home-dir "${runner_home}" --shell /bin/bash "${runner_user}"
fi
runner_group=$(id -gn "${runner_user}")
install -d -o "${runner_user}" -g "${runner_group}" -m 0755 "${runner_home}"
curl --fail --location --retry 5 --retry-all-errors --output "${archive}" %q
echo %q | sha256sum --check --strict
tar --extract --gzip --file "${archive}" --directory "${runner_home}"
rm -f "${archive}"
"${runner_home}/bin/installdependencies.sh"
chown -R "${runner_user}:${runner_group}" "${runner_home}"
systemctl daemon-reload
systemctl enable --now github-actions-runner.service
`, c.RunnerUser, c.RunnerHome, c.Artifact.URL, c.Artifact.SHA256+"  /tmp/actions-runner.tar.gz")

	config.WriteFiles = append([]cloudFile{{
		Path:        "/usr/local/sbin/install-actions-runner",
		Owner:       "root:root",
		Permissions: "0700",
		Encoding:    "b64",
		Content:     base64.StdEncoding.EncodeToString([]byte(installScript)),
	}}, config.WriteFiles...)
	config.RunCmd = [][]string{{"/usr/local/sbin/install-actions-runner"}}
	return marshalCloudConfig(config)
}

func marshalCloudConfig(config cloudConfig) ([]byte, error) {
	data, err := yaml.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal cloud-init: %w", err)
	}
	return append([]byte("#cloud-config\n"), data...), nil
}

func validateGuestPath(path string) error {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\n\r\x00 \t'\"`$\\") {
		return fmt.Errorf("runner home must be a safe absolute path")
	}
	return nil
}
