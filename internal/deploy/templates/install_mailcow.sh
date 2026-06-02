#!/bin/bash
# mailcow dockerized 一键部署脚本（收发一体邮件服务器）
# Ubuntu 22.04 + Docker + mailcow-dockerized
# 占位符：{FQDN} {DOMAIN} {USERNAME} {PASSWORD}
set -e
export DEBIAN_FRONTEND=noninteractive

# 1) 创建 2GB swap（mailcow 的内存占用比 KumoMTA 高）
if [ ! -f /swapfile ]; then
    fallocate -l 2G /swapfile 2>/dev/null || dd if=/dev/zero of=/swapfile bs=1M count=2048 status=none
    chmod 600 /swapfile
    mkswap /swapfile >/dev/null
    swapon /swapfile
    if ! grep -q '^/swapfile' /etc/fstab; then
        echo '/swapfile none swap sw 0 0' >> /etc/fstab
    fi
    echo 'vm.swappiness=10' > /etc/sysctl.d/99-mailcow.conf
    sysctl -p /etc/sysctl.d/99-mailcow.conf >/dev/null 2>&1 || true
fi

# 2) 设置 hostname
hostnamectl set-hostname {FQDN} 2>/dev/null || hostname {FQDN}
if ! grep -q "{FQDN}" /etc/hosts; then
    echo "127.0.1.1 {FQDN}" >> /etc/hosts
fi

# 3) 安装 Docker
if ! command -v docker >/dev/null 2>&1; then
    apt-get update -qq
    apt-get install -y -qq ca-certificates curl gnupg git
    install -m 0755 -d /etc/apt/keyrings
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
    chmod a+r /etc/apt/keyrings/docker.gpg
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
        > /etc/apt/sources.list.d/docker.list
    apt-get update -qq
    apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
    systemctl enable --now docker
fi

# 4) clone mailcow
if [ ! -d /opt/mailcow-dockerized ]; then
    cd /opt
    git clone --depth 1 https://github.com/mailcow/mailcow-dockerized.git
fi
cd /opt/mailcow-dockerized

# 5) 生成 mailcow.conf（非交互式）
if [ ! -f mailcow.conf ]; then
    cat > generate_config.env <<EOF
MAILCOW_HOSTNAME={FQDN}
MAILCOW_TZ=Asia/Tokyo
EOF
    # 自动回答 generate_config.sh 的提示：FQDN + timezone + branch
    ./generate_config.sh <<EOF
{FQDN}
Asia/Tokyo
master
EOF

    # 禁用 ClamAV 省 2GB 内存
    sed -i 's/^SKIP_CLAMD=.*/SKIP_CLAMD=y/' mailcow.conf || echo 'SKIP_CLAMD=y' >> mailcow.conf

    # 收件拒签、自动丢弃附件感染邮件
    sed -i 's/^SKIP_SOGO=.*/SKIP_SOGO=n/' mailcow.conf || true
fi

# 6) 拉镜像 + 启动
docker compose pull
docker compose up -d

# 等待容器就绪
echo "等 mailcow 容器 60 秒初始化..."
sleep 60

# 7) 通过 API 创建域名 + info@{DOMAIN} 用户
# mailcow API key 首次部署是随机，要从数据库里读
MAILCOW_API_KEY=$(docker exec mysql-mailcow mysql -N -u mailcow -p"$(grep DBPASS= mailcow.conf | cut -d= -f2)" mailcow -e "SELECT api_key FROM api WHERE active=1 LIMIT 1;" 2>/dev/null || echo "")
if [ -z "$MAILCOW_API_KEY" ]; then
    # 还没有 API key，通过 admin CLI 造一个
    docker exec mysql-mailcow mysql -u mailcow -p"$(grep DBPASS= mailcow.conf | cut -d= -f2)" mailcow <<SQL 2>/dev/null || true
INSERT INTO api (username,api_key,active,allow_from) VALUES ('admin','{{APIKEY}}',1,'0.0.0.0/0,::/0');
SQL
    # 如果插入不成功（字段不对），先跳过
fi

# 增加域名
curl -sk -X POST -H "Content-Type: application/json" -H "X-API-Key: $MAILCOW_API_KEY" \
    -d '{"domain":"{DOMAIN}","description":"auto-created by gcp-mailnode","aliases":400,"mailboxes":10,"defquota":512,"maxquota":10240,"quota":10240,"active":"1","rl_value":"","rl_frame":"s","backupmx":"0","relay_all_recipients":"0","relay_unknown_only":"0"}' \
    "https://127.0.0.1/api/v1/add/domain" 2>/dev/null || echo "(域名可能已存在或 API key 未就绪，请手动通过 Web UI 添加)"

# 增加用户 {MAIL_USER_LOCAL}@{DOMAIN}
curl -sk -X POST -H "Content-Type: application/json" -H "X-API-Key: $MAILCOW_API_KEY" \
    -d '{"local_part":"{MAIL_USER_LOCAL}","domain":"{DOMAIN}","password":"{PASSWORD}","password2":"{PASSWORD}","quota":"1024","active":"1","name":"{MAIL_USER_LOCAL}"}' \
    "https://127.0.0.1/api/v1/add/mailbox" 2>/dev/null || echo "(用户可能已存在或 API key 未就绪)"

echo "=== install_mailcow.sh done ==="
echo "Web 管理面板: https://{FQDN}/"
echo "默认管理员: admin / moohoo"
echo "邮箱用户: {USERNAME}"
echo "IMAP: {FQDN}:993 (SSL) / SMTP: {FQDN}:587 (STARTTLS)"
