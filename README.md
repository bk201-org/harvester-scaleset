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
make build
make test
```

The binary is written to `bin/harvester-scaleset`. Override `OUTPUT_DIR`, `BINARY`, `VERSION`, or `COMMIT_SHA` when needed.

## Container image

Build a non-root, distroless container image with:

```bash
make docker-build
```

Set `IMAGE` to choose another image name or tag. The container accepts the same flags and environment variables as the binary. Kubeconfig and token files mounted from the host must be readable by the container's non-root user; alternatively, run it with the host user's UID and GID:

```bash
docker run --rm \
  --user "$(id -u):$(id -g)" \
  --mount type=bind,src=/secure/path/github-token,dst=/run/secrets/github-token,readonly \
  --mount type=bind,src=/secure/path/harvester-kubeconfig,dst=/run/secrets/kubeconfig,readonly \
  harvester-scaleset:dev \
  --url https://github.com/example/actions-repository \
  --name harvester-runners \
  --token-file /run/secrets/github-token \
  --kubeconfig /run/secrets/kubeconfig \
  --runner-image default/ubuntu-24.04-actions-runner \
  --network default/vm-network \
  --skip-runner-install
```

## Validate configuration

Validation checks the runner release and Harvester namespace, image, network, and configured keypair without creating a GitHub scale set or cluster resources. Release resolution is omitted when `--skip-runner-install` is set:

```bash
./harvester-scaleset \
  --validate-only \
  --url https://github.com/example/actions-repository \
  --name harvester-runners \
  --token-file /secure/path/github-token \
  --kubeconfig /secure/path/harvester-kubeconfig \
  --runner-image-display-name default/ubuntu-24.04-actions-runner-amd64.qcow2 \
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
  --ssh-key default/hvst-dev-pubkey \
  --ssh-username ubuntu \
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
| `--runner-image` | `HARVESTER_RUNNER_IMAGE` | Kubernetes resource name; one image selector required |
| `--runner-image-display-name` | `HARVESTER_RUNNER_IMAGE_DISPLAY_NAME` | Exact `.spec.displayName`; one image selector required |
| `--network` | `HARVESTER_NETWORK` | required |
| `--ssh-key` | `HARVESTER_KEYPAIR` | optional existing `namespace/name` |
| `--ssh-username` | `HARVESTER_SSH_USERNAME` | `--runner-user` |
| `--extra-host HOST=IP` | `HARVESTER_EXTRA_HOSTS` | none; comma-separated or repeated |
| `--extra-ca-cert-file PATH` | `HARVESTER_EXTRA_CA_CERT_FILES` | none; comma-separated or repeated |
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

CLI values take precedence over their corresponding environment variables. Specify exactly one image selector, through a flag or environment variable: `--runner-image` selects a Kubernetes resource name, while `--runner-image-display-name` selects an exact `.spec.displayName`. Both accept a name in the runner namespace or `namespace/name`. Duplicate display names in the selected namespace are rejected. The controller resolves a display name to the actual image resource name before creating runner disks. The network value is a Kubernetes resource name, either in the runner namespace or as `namespace/name`.

When `--ssh-key` is set, the controller checks that the Harvester `KeyPair` exists and has a public key during validation and before each VM is created. It adds that key to the `ssh_authorized_keys` of `--ssh-username` through the VM's cloud-init data. The SSH account defaults to `--runner-user`; set `--ssh-username` to use a different guest account. Cloud-init creates the account if it does not already exist. The SSH username must be a non-root Linux account. The keypair may be in a different namespace from the runner VMs.

Add runner-only host mappings and trusted PEM certificates with repeatable flags:

```bash
harvester-scaleset \
  --url https://github.com/OWNER/REPO \
  --name harvester-runners \
  --token-file /run/secrets/github-pat \
  --kubeconfig /run/secrets/harvester-kubeconfig \
  --runner-image default/ubuntu \
  --network default/runner-network \
  --extra-host git.internal=192.0.2.20 \
  --extra-host registry.internal=2001:db8::20 \
  --extra-ca-cert-file /etc/runner-ca/internal.pem
```

`HOST` must be a DNS hostname and `IP` must be IPv4 or IPv6. The corresponding environment variables accept comma-separated values; flags take precedence when supplied. A certificate file may contain one certificate or a PEM bundle. The file paths are local to the controller process; mount them there when running in a container. The controller reads and validates these files once at startup, including during `--validate-only`, and each new VM receives the certificates through its cloud-init Secret. Cloud-init appends the mappings to the guest's existing `/etc/hosts` and adds the certificates to its system CA trust store without removing default CAs. The guest image must have cloud-init's CA trust tooling, including the OS `ca-certificates` package, installed before first boot. The bundled Ubuntu runner image already includes it. Containers started by jobs manage their own trust stores.

When `--skip-runner-install` is enabled, the selected image must already contain an executable `<runner-home>/run.sh` owned for use by `<runner-user>`. Cloud-init still writes the one-time JIT environment file and systemd service; it fails early if the baked runner executable is absent.

## Security and ownership

The GitHub PAT remains in the controller process. A runner VM receives its short-lived JIT configuration and, when configured, the SSH public key and extra CA certificates through a per-VM Kubernetes Secret. All controller resources are labeled with their GitHub scale-set ID, and cleanup selects those labels rather than deleting arbitrary namespace resources.

The kubeconfig identity needs get permission for images when using `--runner-image` or list permission when using `--runner-image-display-name` in the selected image namespace; get permission for network attachment definitions in the selected network namespace; get permission for KeyPairs in the selected keypair namespace when `--ssh-key` is used; and get/list/create/update/delete permission for VirtualMachines, Secrets, and PVCs in the runner namespace. Anyone who can read the cloud-init Secret or VM console during bootstrap may obtain the JIT configuration.

## Development

```bash
go test ./...
go vet ./...
```

See [PLAN.md](PLAN.md) for the architecture, reconciliation model, test phases, and remaining production decisions.
