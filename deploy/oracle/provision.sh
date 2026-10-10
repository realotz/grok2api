#!/usr/bin/env bash
set -Eeuo pipefail
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH
umask 077

# 安装专用部署入口和公钥，仅迁移线上 Compose 的镜像字段。
[[ $EUID == 0 && $# == 2 ]] || exit 64
scripts=$1
public_key_file=$2
[[ -f $scripts/deploy.sh && -f $scripts/ci-deploy.sh && -f $public_key_file ]]
public_key=$(<"$public_key_file")
[[ $public_key =~ ^ssh-ed25519\ [A-Za-z0-9+/=]+(\ [A-Za-z0-9@._-]+)?$ ]] || exit 64

cd /opt/grok2api-go
exec 9>/opt/grok2api-go/deploy.lock
flock -n 9 || exit 75
[[ ! -e .env ]]
python3 - <<'PY'
import json
import pathlib
import subprocess

# 以当前有效配置为准，只替换镜像标量，并核对规范化 Compose 完全一致。
path = pathlib.Path("/opt/grok2api-go/compose.yaml")
old = path.read_text()
image = "grok2api:oracle-20261010-2b824139-d8a01802"
line = "    image: " + image + "\n"
assert old.count(line) == 1, "Unexpected production image"
before = json.loads(subprocess.check_output(
    ["docker", "compose", "-f", str(path), "config", "--format", "json"]))
path.with_name("compose.before-ci.yaml").write_text(old)
path.write_text(old.replace(line, "    image: \"${GROK2API_IMAGE:-" + image + "}\"\n"))
after = json.loads(subprocess.check_output(
    ["docker", "compose", "-f", str(path), "config", "--format", "json"]))
assert before == after, "Compose configuration changed"
print("Compose configuration preserved; image variable enabled")
PY
install -o root -g root -m 0700 "$scripts/deploy.sh" ./deploy.sh
install -o root -g root -m 0700 "$scripts/ci-deploy.sh" ./ci-deploy.sh

if ! id grok2api-deploy >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash --password '*' grok2api-deploy
fi
[[ $(id -nG grok2api-deploy) == grok2api-deploy ]] || exit 65
install -d -o root -g root -m 0755 /home/grok2api-deploy/.ssh
printf 'restrict,command="sudo -n /opt/grok2api-go/ci-deploy.sh \\"$SSH_ORIGINAL_COMMAND\\"" %s\n' "$public_key" \
  > /home/grok2api-deploy/.ssh/authorized_keys
chown root:root /home/grok2api-deploy/.ssh/authorized_keys
chmod 0644 /home/grok2api-deploy/.ssh/authorized_keys
printf 'grok2api-deploy ALL=(root) NOPASSWD: /opt/grok2api-go/ci-deploy.sh\n' \
  > /etc/sudoers.d/grok2api-deploy
chmod 0440 /etc/sudoers.d/grok2api-deploy
visudo -cf /etc/sudoers.d/grok2api-deploy
restorecon -RF /home/grok2api-deploy/.ssh /opt/grok2api-go/deploy.sh \
  /opt/grok2api-go/ci-deploy.sh /etc/sudoers.d/grok2api-deploy
printf 'Restricted grok2api-deploy account provisioned\n'
