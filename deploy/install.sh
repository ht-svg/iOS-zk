#!/usr/bin/env bash
# 授权码后台一键部署（Linux + systemd + Caddy）
#
# 用法：
#   sudo ./install.sh license.example.com
#
# 做了什么：
#   1. 创建 license 用户与 /opt/license-admin 目录
#   2. 安装 license-admin 二进制（与本脚本同目录）
#   3. 生成 LICENSE_API_TOKEN 与管理员密码，写入 /etc/license-admin/env
#   4. 安装并启动 systemd 服务
#   5. 打印构建客户端所需的 ldflags
set -euo pipefail

DOMAIN="${1:-}"
if [[ -z "$DOMAIN" ]]; then
  echo "用法：sudo $0 <你的域名>"
  exit 1
fi

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_SRC="$SRC_DIR/license-admin-linux-amd64"
APP_DIR=/opt/license-admin
DATA_DIR="$APP_DIR/data"
ENV_DIR=/etc/license-admin
ENV_FILE="$ENV_DIR/env"

if [[ ! -f "$BIN_SRC" ]]; then
  echo "未找到 $BIN_SRC，请先把 license-admin-linux-amd64 放到本脚本同目录"
  exit 1
fi

echo "==> 创建用户与目录"
id -u license >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin license
install -d -o license -g license -m 750 "$APP_DIR" "$DATA_DIR"
install -d -m 750 "$ENV_DIR"

echo "==> 安装二进制"
install -o license -g license -m 755 "$BIN_SRC" "$APP_DIR/license-admin"

echo "==> 生成凭据"
ADMIN_PASSWORD="$(head -c 18 /dev/urandom | base64 | tr -d '/+=' | cut -c1-20)"
API_TOKEN="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
if [[ -f "$ENV_FILE" ]]; then
  echo "    已存在 $ENV_FILE，保留原凭据"
else
  cat > "$ENV_FILE" <<EOF
LICENSE_ADMIN_PASSWORD=$ADMIN_PASSWORD
LICENSE_API_TOKEN=$API_TOKEN
EOF
  chmod 600 "$ENV_FILE"
fi
# 读取（可能是既有的）凭据用于最后打印
# shellcheck disable=SC1090
source "$ENV_FILE"

echo "==> 安装 systemd 服务"
install -m 644 "$SRC_DIR/license-admin.service" /etc/systemd/system/license-admin.service
systemctl daemon-reload
systemctl enable --now license-admin
sleep 1
systemctl --no-pager --lines=5 status license-admin || true

if command -v caddy >/dev/null 2>&1; then
  echo "==> 配置 Caddy"
  sed "s/license\.example\.com/$DOMAIN/g" "$SRC_DIR/Caddyfile" > /etc/caddy/Caddyfile
  systemctl reload caddy || systemctl restart caddy
else
  echo "!! 未安装 Caddy，跳过 HTTPS 配置。请手动安装：https://caddyserver.com/docs/install"
fi

echo
echo "================ 部署完成 ================"
echo "后台地址：https://$DOMAIN/admin/"
echo "登录用户：admin"
echo "登录密码：$LICENSE_ADMIN_PASSWORD"
echo "接口令牌：$LICENSE_API_TOKEN"
echo
echo "构建客户端时把下面两个值烧进去："
echo "  go build -ldflags \"-s -w \\"
echo "    -X main.defaultLicenseServer=https://$DOMAIN \\"
echo "    -X main.defaultLicenseToken=$LICENSE_API_TOKEN \\"
echo "    -X main.appVersion=0.1.0\" -o 海涛中控.exe ."
echo "=========================================="
echo
echo "提醒：/opt/license-admin/data 里有 records.json、授权码.xlsx 和 signing.key，"
echo "      请定期备份，且 signing.key 一旦丢失，所有已授权设备都需要重新绑定。"
