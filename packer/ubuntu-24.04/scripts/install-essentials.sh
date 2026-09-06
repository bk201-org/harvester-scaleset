#!/usr/bin/env bash
set -euo pipefail

: "${RUNNER_USER:?RUNNER_USER is required}"

export DEBIAN_FRONTEND=noninteractive

apt-get update

apt-get install -y --no-install-recommends \
  qemu-guest-agent
systemctl enable qemu-guest-agent.service

apt-get install -y --no-install-recommends \
  bash \
  ca-certificates \
  curl \
  dnsutils \
  git \
  git-lfs \
  gzip \
  iproute2 \
  iputils-ping \
  jq \
  netcat-openbsd \
  openssh-client \
  openssl \
  python3 \
  sudo \
  tar \
  unzip \
  wget \
  xz-utils \
  zip \
  zstd

install -m 0755 -d /etc/apt/keyrings
curl \
  --fail \
  --location \
  --retry 5 \
  --retry-all-errors \
  https://download.docker.com/linux/ubuntu/gpg \
  --output /etc/apt/keyrings/docker.asc
chmod 0644 /etc/apt/keyrings/docker.asc

# shellcheck disable=SC1091
. /etc/os-release
printf '%s\n' \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu ${VERSION_CODENAME} stable" \
  > /etc/apt/sources.list.d/docker.list

apt-get update
apt-get install -y --no-install-recommends \
  containerd.io \
  docker-buildx-plugin \
  docker-ce \
  docker-ce-cli \
  docker-compose-plugin

if ! id "${RUNNER_USER}" >/dev/null 2>&1; then
  useradd --system --create-home --shell /bin/bash "${RUNNER_USER}"
fi
usermod --append --groups docker "${RUNNER_USER}"

systemctl enable docker.service containerd.service
systemctl start docker.service

docker version
docker buildx version
docker compose version
git lfs install --system
