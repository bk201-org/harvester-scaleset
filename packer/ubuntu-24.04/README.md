# Ubuntu 24.04 runner image

This Packer template turns Ubuntu's Noble amd64 cloud image into a Harvester-importable qcow2 image with the GitHub Actions runner, Docker runtime, and essential workflow utilities already installed.

The image includes Docker Engine, Buildx, Compose v2, Git, Git LFS, SSH, common archive tools, `curl`, `wget`, `jq`, and basic network diagnostics. Project toolchains and infrastructure tools are intentionally left to workflow setup actions. The `runner` user belongs to the `docker` group, which gives jobs root-equivalent access to their disposable VM.

The source image is fixed to:

```text
https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img
```

## Build

Install Packer, QEMU, and KVM support, then run:

```bash
cd packer/ubuntu-24.04
packer init .
packer build .
```

The result is `output/ubuntu-24.04-actions-runner-amd64.qcow2`. The default build resolves the latest stable runner release and verifies its GitHub-published SHA-256 digest. Pin a version when you need predictable runner contents:

```bash
packer build -var runner_version=2.337.0 .
```

On a host without KVM, `-var accelerator=tcg` works but is substantially slower. Other build sizing and output options are documented by `packer inspect .`.

The temporary `packer` account uses a build-only password so Packer can establish SSH. Before shutdown, the template locks that account, removes its sudo access and SSH keys, disables SSH password authentication, clears cloud-init state and the machine ID, and removes SSH host keys so every Harvester VM regenerates unique first-boot state.

## Use with Harvester

Upload the qcow2 result as a Harvester VM image. Point the controller at that image and enable preinstalled-runner mode:

```bash
harvester-scaleset \
  --runner-image default/ubuntu-24.04-actions-runner \
  --skip-runner-install \
  # ...the remaining GitHub and Harvester options
```

In this mode, per-VM cloud-init only injects the short-lived JIT configuration, verifies `/opt/actions-runner/run.sh` exists, installs the ephemeral runner systemd unit, and starts it. The image contract therefore requires the baked `runner` user and `/opt/actions-runner` layout produced by this template.
