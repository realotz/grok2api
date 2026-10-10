#!/usr/bin/env bash
set -Eeuo pipefail
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH
umask 077

# 串行更新固定服务，只接受本仓库 ARM64 digest，保留线上配置和数据。
if [[ $EUID != 0 || $# != 1 ]]; then
  echo "Usage: deploy.sh ghcr.io/realotz/grok2api@sha256:DIGEST (root required)" >&2
  exit 64
fi
image=$1
[[ $image =~ ^ghcr\.io/realotz/grok2api@sha256:[a-f0-9]{64}$ ]] || exit 64

cd /opt/grok2api-go
exec 9>/opt/grok2api-go/deploy.lock
flock -n 9 || { echo "Grok2API deployment already running" >&2; exit 75; }
[[ -f compose.yaml && -f config.yaml ]]
[[ -d data-final-20261010T023040Z && -d quality-guard-final-20261010T023040Z ]]

# CI 的短期 registry token 只进入临时 Docker 配置，退出时清理。
IFS= read -r registry_user
IFS= read -r registry_token
[[ $registry_user =~ ^[A-Za-z0-9_-]+$ && -n $registry_token ]] || exit 64
DOCKER_CONFIG=$(mktemp -d /opt/grok2api-go/.registry.XXXXXX)
export DOCKER_CONFIG
trap 'rm -rf -- "$DOCKER_CONFIG"' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
printf '%s' "$registry_token" | docker login ghcr.io -u "$registry_user" --password-stdin >/dev/null
unset registry_token
docker pull --platform linux/arm64 "$image"

[[ $(docker image inspect "$image" --format '{{.Architecture}}') == arm64 ]] || {
  echo "Image must be ARM64" >&2
  exit 65
}
[[ $(docker image inspect "$image" --format '{{index .Config.Labels "org.opencontainers.image.source"}}') == https://github.com/realotz/grok2api ]] || exit 65
export GROK2API_IMAGE=$image
docker compose -f compose.yaml config --quiet
docker compose -f compose.yaml up -d --no-deps --pull never --wait --wait-timeout 180 grok2api
[[ $(docker inspect grok2api-go --format '{{.Config.Image}}') == "$image" ]]
curl --fail --silent --show-error --max-time 10 http://127.0.0.1:8000/healthz >/dev/null
candidate=$(mktemp /opt/grok2api-go/.deployment.XXXXXX)
printf 'GROK2API_IMAGE=%s\n' "$image" >"$candidate"
mv -f -- "$candidate" /opt/grok2api-go/.env
printf 'Deployed %s\n' "$image"
