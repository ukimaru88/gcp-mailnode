#!/bin/bash
set -e
export DEBIAN_FRONTEND=noninteractive

# 创建 2GB swap（小内存机救命 + 生产机作备份缓冲）
if [ ! -f /swapfile ]; then
    fallocate -l 2G /swapfile 2>/dev/null || dd if=/dev/zero of=/swapfile bs=1M count=2048 status=none
    chmod 600 /swapfile
    mkswap /swapfile >/dev/null
    swapon /swapfile
    if ! grep -q '^/swapfile' /etc/fstab; then
        echo '/swapfile none swap sw 0 0' >> /etc/fstab
    fi
    # 降低 swappiness：只有在实在内存不够时才用 swap
    echo 'vm.swappiness=10' > /etc/sysctl.d/99-kumomta.conf
    sysctl -p /etc/sysctl.d/99-kumomta.conf >/dev/null 2>&1 || true
fi

apt-get update -qq
# zstd is required for reading KumoMTA's archived logs; without it, log
# extraction can look successful while returning zero records.
apt-get install -y -qq curl ca-certificates gnupg lsb-release openssl zstd

# KumoMTA 官方源（Ubuntu 22.04 jammy）
# 2026-04：KumoMTA 不再支持 Debian bookworm。软件模板已切换为 Ubuntu 22.04。
if ! [ -f /etc/apt/sources.list.d/kumomta.list ]; then
    curl -fsSL https://openrepo.kumomta.com/kumomta-ubuntu-22/public.gpg \
        | gpg --yes --dearmor -o /usr/share/keyrings/kumomta.gpg
    chmod 644 /usr/share/keyrings/kumomta.gpg
    curl -fsSL https://openrepo.kumomta.com/files/kumomta-ubuntu22.list \
        -o /etc/apt/sources.list.d/kumomta.list
fi

apt-get update -qq
apt-get install -y -qq kumomta

mkdir -p /opt/kumomta/etc/policy /opt/kumomta/etc/keys/{DOMAIN} /opt/kumomta/etc/tls /var/log/kumomta /var/spool/kumomta

# v0.1.82：log_dir 必须 kumod:kumod 2770（setgid + 0770），否则 kumod 写不进去 → silent fail（日志为空）
# 官方推荐：https://docs.kumomta.com/userguide/installation/security/
chown -R kumod:kumod /var/log/kumomta /var/spool/kumomta 2>/dev/null || true
chmod 2770 /var/log/kumomta 2>/dev/null || true

hostnamectl set-hostname {FQDN} 2>/dev/null || hostname {FQDN}

# /etc/hosts
if ! grep -q "{FQDN}" /etc/hosts; then
    echo "127.0.1.1 {FQDN}" >> /etc/hosts
fi

# 自签 TLS 证书（用于 465/587）
if [ ! -f /opt/kumomta/etc/tls/cert.pem ]; then
    openssl req -x509 -nodes -days 3650 -newkey rsa:2048 \
        -keyout /opt/kumomta/etc/tls/key.pem \
        -out /opt/kumomta/etc/tls/cert.pem \
        -subj "/CN={FQDN}" 2>/dev/null
fi
chown -R kumod:kumod /opt/kumomta/etc/tls 2>/dev/null || true
chmod 600 /opt/kumomta/etc/tls/key.pem

echo "=== install_kumomta.sh done ==="
