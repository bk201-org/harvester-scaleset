# Harvester GitHub Actions Runner Scale Set

`harvester-scaleset` is a Go controller that implements a GitHub Actions runner scale set with ephemeral Harvester virtual machines. It uses GitHub's JIT runner configuration, creates one VM per runner, installs or starts the Actions runner through cloud-init, and deletes the VM and its boot disk when the job completes.

> This repository is under active development. Use a dedicated namespace, image, network, and GitHub test repository until the end-to-end behavior has been validated for your Harvester version.

## Requirements

The scale-set administrator—the person operating this controller—provides:

- a GitHub PAT allowed to administer Actions runners for the target repository or organization;
- a Harvester kubeconfig;
- a cloud-init-capable Linux image;
- a Harvester VM network with DHCP, DNS, and outbound HTTPS access; and
- a unique scale-set name.

For the default first-boot installation path, the guest image must have `systemd` and one of `apt-get`, `zypper`, `dnf`, or `yum`. Cloud-init installs `curl`, `tar`, CA certificates, the GitHub Actions runner, and the runner's native dependencies. The controller resolves the selected release and SHA-256 digest before it creates the GitHub scale set. Runner-ready images used with `--skip-runner-install` need only `systemd`, the configured runner user, and an executable runner at the configured home.

First boot downloads and installs the runner by default, so a new VM can take several minutes to register. For lower startup latency, build the included [Ubuntu 24.04 runner image](packer/ubuntu-24.04/README.md) and pass `--skip-runner-install`; cloud-init will then inject the JIT configuration and start the baked runner without resolving or downloading a release.

## Build

```bash
go build -o harvester-scaleset ./cmd/harvester-scaleset
```

## Validate configuration

Validation checks the runner release and Harvester namespace, image, and network without creating a GitHub scale set or cluster resources. Release resolution is omitted when `--skip-runner-install` is set:

```bash
./harvester-scaleset \
  --validate-only \
  --url https://github.com/example/actions-repository \
  --name harvester-runners \
  --token-file /secure/path/github-token \
  --kubeconfig /secure/path/harvester-kubeconfig \
  --runner-image default/ubuntu-24.04-actions-runner \
  --network default/vm-network
```

## Run

```bash
./harvester-scaleset \
  --url https://github.com/example/actions-repository \
  --name harvester-runners \
  --labels harvester-runners \
  --token-file /secure/path/github-token \
  --kubeconfig /secure/path/harvester-kubeconfig \
  --namespace default \
  --runner-image default/ubuntu \
  --network default/vm-network \
  --cpu 2 \
  --memory 4Gi \
  --disk-size 20Gi \
  --skip-runner-install \
  --min-runners 0 \
  --max-runners 10
```

Send `SIGINT` or `SIGTERM` for graceful, label-scoped cleanup. The current behavior matches the upstream Docker example and deletes busy runner VMs rather than draining them.

## Configuration

| CLI flag | Environment variable | Default |
|---|---|---|
| `--url` | `GITHUB_CONFIG_URL` | required |
| `--name` | `GITHUB_SCALESET_NAME` | required |
| `--token-file` | `GITHUB_TOKEN_FILE` | `GITHUB_TOKEN` may contain the token directly |
| `--kubeconfig` | `KUBECONFIG` | `HARVESTER_KUBECONFIG_B64` may contain base64 instead |
| `--kube-context` | `HARVESTER_KUBE_CONTEXT` | kubeconfig current context |
| `--namespace` | `HARVESTER_NAMESPACE` | `default` |
| `--runner-image` | `HARVESTER_RUNNER_IMAGE` | required |
| `--network` | `HARVESTER_NETWORK` | required |
| `--runner-group` | `GITHUB_RUNNER_GROUP` | `default` |
| `--labels` | `GITHUB_RUNNER_LABELS` | scale-set name |
| `--min-runners` | `MIN_RUNNERS` | `0` |
| `--max-runners` | `MAX_RUNNERS` | `10` |
| `--cpu` | `HARVESTER_CPU` | `2` |
| `--memory` | `HARVESTER_MEMORY` | `4Gi` |
| `--disk-size` | `HARVESTER_DISK_SIZE` | `20Gi` |
| `--runner-version` | `ACTIONS_RUNNER_VERSION` | `latest` |
| `--runner-arch` | `ACTIONS_RUNNER_ARCH` | `x64` |
| `--runner-user` | `HARVESTER_RUNNER_USER` | `runner` |
| `--runner-home` | `HARVESTER_RUNNER_HOME` | `/opt/actions-runner` |
| `--skip-runner-install` | `HARVESTER_SKIP_RUNNER_INSTALL` | `false` |
| `--vm-create-timeout` | `VM_CREATE_TIMEOUT` | `10m` |
| `--vm-delete-timeout` | `VM_DELETE_TIMEOUT` | `5m` |
| `--reconcile-interval` | `RECONCILE_INTERVAL` | `15s` |
| `--max-parallel-creates` | `MAX_PARALLEL_CREATES` | `5` |
| `--log-level` | `LOG_LEVEL` | `info` |
| `--log-format` | `LOG_FORMAT` | `text` |

CLI values take precedence over environment variables. Image and network values are Kubernetes resource names, either in the runner namespace or written as `namespace/name`.

When `--skip-runner-install` is enabled, the selected image must already contain an executable `<runner-home>/run.sh` owned for use by `<runner-user>`. Cloud-init still writes the one-time JIT environment file and systemd service; it fails early if the baked runner executable is absent.

## Security and ownership

The GitHub PAT remains in the controller process. A runner VM receives only its short-lived JIT configuration through a per-VM Kubernetes Secret. All controller resources are labeled with their GitHub scale-set ID, and cleanup selects those labels rather than deleting arbitrary namespace resources.

The kubeconfig identity needs permission in the runner namespace to read images and network attachment definitions and to get/list/create/update/delete VirtualMachines, Secrets, and PVCs. Anyone who can read the cloud-init Secret or VM console during bootstrap may obtain the JIT configuration.

## Development

```bash
go test ./...
go vet ./...
```

See [PLAN.md](PLAN.md) for the architecture, reconciliation model, test phases, and remaining production decisions.
