#!/usr/bin/env bash
set -Eeuo pipefail
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH

# SSH 强制命令仅传入一个原始命令参数，拒绝 shell 和其他服务镜像。
if [[ $EUID == 0 && $# == 1 && $1 =~ ^deploy\ (ghcr\.io/realotz/grok2api@sha256:[a-f0-9]{64})$ ]]; then
  exec /opt/grok2api-go/deploy.sh "${BASH_REMATCH[1]}"
fi
echo "Only immutable Grok2API deployments are permitted" >&2
exit 64
