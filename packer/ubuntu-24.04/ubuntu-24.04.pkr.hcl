packer {
  required_plugins {
    qemu = {
      source  = "github.com/hashicorp/qemu"
      version = "~> 1.1"
    }
  }
}

variable "accelerator" {
  type        = string
  description = "QEMU accelerator. Use tcg on a build host without KVM."
  default     = "kvm"
}

variable "build_password" {
  type        = string
  description = "Temporary password for the Packer-only SSH account. The account is locked before the image is emitted."
  default     = "packer"
  sensitive   = true
}

variable "cpus" {
  type        = number
  description = "vCPU count used while building the image."
  default     = 2
}

variable "disk_size" {
  type        = string
  description = "Virtual size of the output disk."
  default     = "20G"
}

variable "headless" {
  type        = bool
  description = "Run QEMU without a graphical console."
  default     = true
}

variable "memory" {
  type        = number
  description = "Memory in MiB used while building the image."
  default     = 2048
}

variable "output_directory" {
  type        = string
  description = "Directory for the generated qcow2 image, relative to this template."
  default     = "output"
}

variable "runner_version" {
  type        = string
  description = "GitHub Actions runner version without a v prefix, or latest."
  default     = "latest"

  validation {
    condition     = var.runner_version == "latest" || can(regex("^[0-9]+\\.[0-9]+\\.[0-9]+$", trimprefix(var.runner_version, "v")))
    error_message = "Runner version must be latest or a semantic version such as 2.337.0."
  }
}

variable "source_checksum" {
  type        = string
  description = "Checksum for source_url. The default reads Ubuntu's published release checksum list."
  default     = "file:https://cloud-images.ubuntu.com/noble/current/SHA256SUMS"
}

variable "source_url" {
  type        = string
  description = "Ubuntu Noble amd64 cloud image."
  default     = "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img"
}

locals {
  cloud_init_user_data = <<-EOF
    #cloud-config
    users:
      - default
      - name: packer
        gecos: Temporary Packer build user
        groups: [adm]
        shell: /bin/bash
        lock_passwd: false
        plain_text_passwd: ${jsonencode(var.build_password)}
    ssh_pwauth: true
    write_files:
      - path: /etc/sudoers.d/90-packer-build
        owner: root:root
        permissions: '0440'
        content: |
          packer ALL=(ALL) NOPASSWD:ALL
    EOF
}

source "qemu" "ubuntu_24_04" {
  accelerator = var.accelerator
  boot_wait   = "5s"
  cd_content = {
    "meta-data" = "instance-id: packer-ubuntu-24-04\nlocal-hostname: runner-image\n"
    "user-data" = local.cloud_init_user_data
  }
  cd_label         = "cidata"
  cpus             = var.cpus
  disk_compression = true
  disk_discard     = "unmap"
  disk_image       = true
  disk_interface   = "virtio"
  disk_size        = var.disk_size
  format           = "qcow2"
  headless         = var.headless
  iso_checksum     = var.source_checksum
  iso_url          = var.source_url
  memory           = var.memory
  net_device       = "virtio-net"
  output_directory = "${path.root}/${var.output_directory}"
  shutdown_command = "sudo /bin/sh -c 'passwd --lock packer; rm -rf /home/packer/.ssh; rm -f /etc/sudoers.d/90-packer-build; printf \"PasswordAuthentication no\\n\" > /etc/ssh/sshd_config.d/00-runner-image-hardening.conf; shutdown -P now'"
  ssh_password     = var.build_password
  ssh_timeout      = "20m"
  ssh_username     = "packer"
  vm_name          = "ubuntu-24.04-actions-runner-amd64.qcow2"
}

build {
  name    = "ubuntu-24.04-actions-runner"
  sources = ["source.qemu.ubuntu_24_04"]

  provisioner "shell" {
    inline = ["cloud-init status --wait"]
  }

  provisioner "shell" {
    environment_vars = [
      "RUNNER_USER=runner",
    ]
    execute_command = "chmod +x {{ .Path }}; {{ .Vars }} sudo -E {{ .Path }}"
    script          = "${path.root}/scripts/install-essentials.sh"
  }

  provisioner "shell" {
    environment_vars = [
      "RUNNER_ARCH=x64",
      "RUNNER_HOME=/opt/actions-runner",
      "RUNNER_USER=runner",
      "RUNNER_VERSION=${trimprefix(var.runner_version, "v")}",
    ]
    execute_command = "chmod +x {{ .Path }}; {{ .Vars }} sudo -E {{ .Path }}"
    script          = "${path.root}/scripts/install-runner.sh"
  }

  provisioner "shell" {
    inline = [
      "sudo cloud-init clean --logs --machine-id",
      "sudo rm -f /etc/ssh/ssh_host_*",
      "sudo rm -rf /tmp/* /var/tmp/*",
      "sudo apt-get clean",
      "sudo rm -rf /var/lib/apt/lists/*",
      "sudo sync",
    ]
  }
}
