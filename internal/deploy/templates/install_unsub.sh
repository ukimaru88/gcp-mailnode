#!/bin/bash
# v0.1.74：在 KumoMTA VPS 上装退订接收服务（caddy 反代 + unsub-server Go 二进制 + sqlite）
# 占位符：{FQDN}, {DOMAIN}, {UNSUB_SECRET}
# v0.1.78：unsub-server 二进制改由 SSH UploadBytes 上传到 /tmp/unsub-server，
#          脚本本身只剩 mv，避免单一 SSH command 超 ARG_MAX 撞 EOF。
set -e
export DEBIAN_FRONTEND=noninteractive

echo "=== install_unsub: 开始 ==="

# 1. 装 caddy（官方源）
if ! command -v caddy >/dev/null 2>&1; then
    apt-get install -y -qq curl gnupg debian-keyring debian-archive-keyring apt-transport-https
    curl -fsSL "https://dl.cloudsmith.io/public/caddy/stable/gpg.key" \
        | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    curl -fsSL "https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt" \
        | tee /etc/apt/sources.list.d/caddy-stable.list >/dev/null
    apt-get update -qq
    apt-get install -y -qq caddy
fi

# 2. 创建 unsub 用户 + 目录
id -u unsub >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin unsub
mkdir -p /var/lib/unsub /etc/unsub /opt/unsub
chown unsub:unsub /var/lib/unsub
chmod 750 /var/lib/unsub

# 3. 写 HMAC 密钥（每台 VPS 独立生成，部署时由 Go 端注入）
echo -n "{UNSUB_SECRET}" > /etc/unsub/secret.key
chmod 600 /etc/unsub/secret.key
chown unsub:unsub /etc/unsub/secret.key

# 4. 安装 unsub-server 二进制（由 Go 端 ssh.UploadBytes 预先上传到 /tmp/unsub-server）
if [ ! -s /tmp/unsub-server ]; then
    echo "[ERROR] /tmp/unsub-server 未上传或为空（Go 端 UploadBytes 应在跑此脚本前完成）"
    exit 1
fi
mv /tmp/unsub-server /opt/unsub/unsub-server
chmod 755 /opt/unsub/unsub-server

# 5. systemd unit
cat > /etc/systemd/system/unsub-server.service <<'UNIT'
[Unit]
Description=Unsubscribe HTTP receiver
After=network.target

[Service]
Type=simple
User=unsub
Group=unsub
EnvironmentFile=-/etc/unsub/env
Environment=UNSUB_LISTEN=127.0.0.1:8080
Environment=UNSUB_DB=/var/lib/unsub/unsub.db
ExecStart=/opt/unsub/unsub-server
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT

# UNSUB_SECRET 不能直接写在 unit 里（避免被 ps/systemctl cat 看到），用 EnvironmentFile
cat > /etc/unsub/env <<EOF
UNSUB_SECRET=$(cat /etc/unsub/secret.key)
EOF
chmod 600 /etc/unsub/env
chown unsub:unsub /etc/unsub/env

# 6. Caddyfile：用 root 域 + Let's Encrypt 自动签证书
cat > /etc/caddy/Caddyfile <<'CADDYFILE'
{FQDN_BARE} {
    encode gzip
    log {
        output file /var/log/caddy/access.log {
            roll_size 10mb
            roll_keep 5
        }
    }

    handle /u* {
        reverse_proxy 127.0.0.1:8080
    }

    handle /healthz {
        reverse_proxy 127.0.0.1:8080
    }

    handle {
        respond "OK" 200
    }
}
CADDYFILE

# 7. 启动服务
systemctl daemon-reload
systemctl enable --now unsub-server.service
sleep 2
if ! systemctl is-active --quiet unsub-server; then
    echo "[ERROR] unsub-server failed to start"
    journalctl -u unsub-server -n 20 --no-pager
    exit 1
fi

systemctl restart caddy
sleep 2
if ! systemctl is-active --quiet caddy; then
    echo "[ERROR] caddy failed to start"
    journalctl -u caddy -n 20 --no-pager
    exit 1
fi

# 8. 防火墙开 80/443
for p in 80 443; do
    if command -v ufw >/dev/null 2>&1; then
        ufw allow "${p}/tcp" >/dev/null 2>&1 || true
    fi
    if command -v iptables >/dev/null 2>&1; then
        iptables -C INPUT -p tcp --dport "${p}" -j ACCEPT >/dev/null 2>&1 || \
            iptables -I INPUT -p tcp --dport "${p}" -j ACCEPT >/dev/null 2>&1 || true
    fi
done

# 9. 健康检查（local loopback）
sleep 3
if curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
    echo "=== install_unsub: OK (unsub-server health ok) ==="
else
    echo "[WARN] unsub-server healthz failed"
fi

echo "=== install_unsub.sh done ==="
