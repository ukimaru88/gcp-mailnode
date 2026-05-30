#!/bin/bash
# Postfix + OpenDKIM 一站式部署
# 端口自：mail-toolkit/internal/deploy/templates/full_deploy.sh
# 适配：占位符 {{x}} → {X}；hostname/domain 拆分；DKIM 公钥走 stdout（与 dkim_setup.sh 一致）；移除 unsub/monitor 相关分支
set -e
export DEBIAN_FRONTEND=noninteractive

log_info() {
  echo "[INFO] $*"
}

log_warn() {
  echo "[WARN] $*"
}

log_error() {
  echo "[ERROR] $*"
}

apt_retry() {
  local max_try=4
  local i=1
  local rc=0

  while [ "$i" -le "$max_try" ]; do
    if "$@"; then
      return 0
    fi
    rc=$?
    log_warn "apt command failed (rc=${rc}) try ${i}/${max_try}: $*"
    if [ "$rc" -eq 100 ]; then
      dpkg --configure -a >/dev/null 2>&1 || true
      apt-get -f install -y >/dev/null 2>&1 || true
    fi
    sleep 3
    i=$((i + 1))
  done

  return "$rc"
}

ensure_firewall_allow() {
  local port="$1"

  if command -v ufw >/dev/null 2>&1; then
    ufw allow "${port}/tcp" >/dev/null 2>&1 || true
    if ufw status | grep -q "Status: active"; then
      log_info "ufw allowed tcp/${port} (active)"
    else
      log_info "ufw allowed tcp/${port} (inactive, rule persisted; will apply when enabled)"
    fi
    return
  fi

  if command -v firewall-cmd >/dev/null 2>&1; then
    firewall-cmd --permanent --add-port="${port}/tcp" >/dev/null 2>&1 || true
    if firewall-cmd --state >/dev/null 2>&1; then
      firewall-cmd --reload >/dev/null 2>&1 || true
      log_info "firewalld allowed tcp/${port} (active)"
    else
      log_info "firewalld allowed tcp/${port} (inactive, rule persisted; will apply when enabled)"
    fi
    return
  fi

  if command -v iptables >/dev/null 2>&1; then
    iptables -C INPUT -p tcp --dport "${port}" -j ACCEPT >/dev/null 2>&1 || \
      iptables -I INPUT -p tcp --dport "${port}" -j ACCEPT >/dev/null 2>&1 || true
    if command -v netfilter-persistent >/dev/null 2>&1; then
      netfilter-persistent save >/dev/null 2>&1 || true
    fi
    log_info "iptables allowed tcp/${port}"
    return
  fi

  log_warn "No supported firewall tool detected, skipped allowing tcp/${port}"
}

check_port_listening() {
  local port="$1"
  if ss -lntp | grep -q ":${port} "; then
    log_info "port ${port} is listening"
    return 0
  fi
  log_error "port ${port} is NOT listening"
  return 1
}

check_local_tcp_connect() {
  local port="$1"
  if timeout 3 bash -c "cat < /dev/null > /dev/tcp/127.0.0.1/${port}" >/dev/null 2>&1; then
    log_info "local tcp connect 127.0.0.1:${port} ok"
    return 0
  fi
  log_error "local tcp connect 127.0.0.1:${port} failed"
  return 1
}

stop_conflicting_mtas() {
  log_info "Stopping conflicting MTA services before Postfix setup"
  for svc in kumomta exim4 sendmail; do
    if systemctl list-unit-files 2>/dev/null | grep -q "^${svc}.service"; then
      systemctl stop "${svc}" 2>/dev/null || true
      systemctl disable "${svc}" 2>/dev/null || true
      log_info "stopped/disabled ${svc}"
    fi
  done

  for proc in kumod exim4 sendmail; do
    if pgrep -x "${proc}" >/dev/null 2>&1; then
      pkill -TERM -x "${proc}" 2>/dev/null || true
      sleep 1
      pkill -KILL -x "${proc}" 2>/dev/null || true
      log_info "killed leftover ${proc}"
    fi
  done
}

ensure_smtp_ports_ready() {
  local failed=0
  local listen_ports="25 587 465 2525"
  local firewall_ports="25 465 587 2525"

  for p in ${firewall_ports}; do
    ensure_firewall_allow "${p}"
  done

  systemctl restart postfix
  systemctl enable postfix

  for p in ${listen_ports}; do
    check_port_listening "${p}" || failed=1
    check_local_tcp_connect "${p}" || failed=1
  done

  if [ "${failed}" -ne 0 ]; then
    log_error "SMTP port self-check failed (25/587/465/2525), stop deployment."
    log_info "Listening SMTP processes:"
    ss -lntp 2>/dev/null | grep -E ':(25|587|465|2525)\b' || true
    exit 1
  fi

  log_info "SMTP ports are ready: 25/587/465/2525"
}

restore_normal_postfix_headers() {
  if ! command -v postconf >/dev/null 2>&1; then
    return 0
  fi

  log_info "[cleanup] restoring normal Postfix header handling"
  rm -f /etc/postfix/header_checks 2>/dev/null || true
  postconf -# "header_checks" 2>/dev/null || postconf -e "header_checks=" 2>/dev/null || true
  postconf -# "mime_header_checks" 2>/dev/null || postconf -e "mime_header_checks=" 2>/dev/null || true
  postconf -# "nested_header_checks" 2>/dev/null || true
  postconf -# "body_checks" 2>/dev/null || true

  # Postfix 模式使用普通 SMTP 头处理：正常添加 Received，正常显示客户端 IP。
  postconf -e "local_header_rewrite_clients = permit_inet_interfaces" 2>/dev/null || true
  postconf -# "remote_header_rewrite_domain" 2>/dev/null || true
  postconf -# "masquerade_domains" 2>/dev/null || true
  postconf -# "masquerade_exceptions" 2>/dev/null || true
  postconf -# "always_add_missing_headers" 2>/dev/null || true
  postconf -# "enable_long_queue_ids" 2>/dev/null || true
  postconf -e "smtpd_sasl_authenticated_header = no" 2>/dev/null || true
  postconf -e "cleanup_service_name = cleanup" 2>/dev/null || true
}

if ! command -v apt-get >/dev/null 2>&1; then
  echo "Only Debian/Ubuntu is supported (apt-get not found)."
  exit 1
fi
if ! command -v systemctl >/dev/null 2>&1; then
  echo "Systemd is required."
  exit 1
fi

stop_conflicting_mtas

# ============================================================
# 变量（gcp 模板渲染替换占位符）
# ============================================================
FQDN="{FQDN}"
DOMAIN="{DOMAIN}"
SERVER_IP="{BIND_IP}"
SELECTOR="{SELECTOR}"
# v0.2.6：{USERNAME} = info@根域名（mail-toolkit 约定）。
# Cyrus SASL sasldb 后端把它整段当 SASL 用户名存（带 @），SMTP 登录用整串。
# 同时为本地邮件路由也建一个纯前缀的系统用户（Maildir 兜底，发件不依赖）。
SASL_USER_FULL="{USERNAME}"
MAIL_USER="${SASL_USER_FULL%@*}"
MAIL_PASS="{PASSWORD}"

restore_normal_postfix_headers

# ============================================================
# Phase 1: 配置主机名
# ============================================================
# 直接写文件 + hostname 命令更新运行时，绕开 hostnamectl/polkit
echo "${FQDN}" > /etc/hostname
hostname "${FQDN}" 2>/dev/null || true
echo "127.0.0.1 localhost" > /etc/hosts
echo "${SERVER_IP} ${FQDN}" >> /etc/hosts
log_info "Hostname configured to ${FQDN} (mydomain=${DOMAIN})"

# ============================================================
# Phase 2: 清理 apt 锁 + 安装依赖
# ============================================================
if command -v postconf >/dev/null 2>&1 && command -v opendkim-genkey >/dev/null 2>&1; then
  log_info "Postfix and OpenDKIM already installed, skipping apt"
else
  log_info "Killing all apt/dpkg processes and clearing locks"
  systemctl stop unattended-upgrades 2>/dev/null || true
  systemctl disable unattended-upgrades 2>/dev/null || true
  systemctl stop apt-daily.timer apt-daily-upgrade.timer 2>/dev/null || true
  systemctl disable apt-daily.timer apt-daily-upgrade.timer 2>/dev/null || true
  killall -9 unattended-upgrade apt apt-get 2>/dev/null || true
  sleep 2
  rm -f /var/lib/dpkg/lock-frontend /var/lib/dpkg/lock /var/lib/apt/lists/lock /var/cache/apt/archives/lock 2>/dev/null || true
  dpkg --configure -a 2>/dev/null || true

  sed -i 's|http://[a-z]*\.archive\.ubuntu\.com|http://archive.ubuntu.com|g' /etc/apt/sources.list 2>/dev/null || true

  log_info "Installing all dependencies"
  if ! apt-get install -y postfix mailutils sasl2-bin libsasl2-modules opendkim opendkim-tools 2>/dev/null; then
    log_info "Direct install failed, running apt-get update"
    apt_retry apt-get update
    apt_retry apt-get install -y postfix mailutils sasl2-bin libsasl2-modules opendkim opendkim-tools
  fi
fi

# ============================================================
# Phase 3: 配置 Postfix
# ============================================================
log_info "Configuring Postfix"

postconf -e "myhostname = ${FQDN}"
postconf -e "mydomain = ${DOMAIN}"
postconf -e "myorigin = \$mydomain"
postconf -e "inet_interfaces = all"
postconf -e "inet_protocols = ipv4"
postconf -e "mydestination = \$myhostname, localhost.\$mydomain, localhost, \$mydomain"
postconf -e "home_mailbox = Maildir/"
postconf -e "smtpd_banner = \$myhostname ESMTP"
postconf -e "smtp_tls_security_level = may"
postconf -e "smtpd_tls_security_level = may"
postconf -e "smtpd_tls_cert_file = /etc/ssl/certs/ssl-cert-snakeoil.pem"
postconf -e "smtpd_tls_key_file = /etc/ssl/private/ssl-cert-snakeoil.key"
postconf -e "smtpd_sasl_auth_enable = yes"
postconf -e "smtpd_sasl_type = cyrus"
postconf -e "smtpd_sasl_path = smtpd"
postconf -e "smtpd_sasl_security_options = noanonymous"
postconf -e "smtpd_sasl_local_domain ="
postconf -e "broken_sasl_auth_clients = yes"
postconf -e "smtpd_tls_auth_only = yes"
postconf -e "smtpd_relay_restrictions = permit_mynetworks,permit_sasl_authenticated,defer_unauth_destination"
postconf -e "smtpd_recipient_restrictions = permit_mynetworks,permit_sasl_authenticated,reject_unauth_destination"
postconf -e "milter_protocol = 6"
postconf -e "milter_default_action = accept"
postconf -e "smtpd_milters = inet:localhost:8891"
postconf -e "non_smtpd_milters = inet:localhost:8891"

# v0.2.6：Cyrus SASL 用 sasldb 后端（不再走 saslauthd-PAM）。
# 这样 SMTP 登录可以直接用 info@根域 整串（PAM 系统用户名带 @ 会失败）。
# pwcheck_method=auxprop + auxprop_plugin=sasldb 是 Cyrus SASL 最简单的纯文件后端，
# 用 saslpasswd2 添加/更新用户。
mkdir -p /etc/postfix/sasl
cat > /etc/postfix/sasl/smtpd.conf <<EOF
pwcheck_method: auxprop
auxprop_plugin: sasldb
mech_list: plain login
EOF

# 把 SASL 用户 info@根域 写入 sasldb。-p 从 stdin 读密码，-c 创建/更新，-f 指定 db 路径。
# sasldb2 默认路径 /etc/sasldb2；Postfix chroot 后还需要 chrooted 副本，下面同步。
touch /etc/sasldb2
chown root:sasl /etc/sasldb2 2>/dev/null || true
chmod 640 /etc/sasldb2
echo "${MAIL_PASS}" | saslpasswd2 -p -c -f /etc/sasldb2 "${SASL_USER_FULL}"

# Postfix chroot 在 /var/spool/postfix；需要在 chroot 里也能读 sasldb2
mkdir -p /var/spool/postfix/etc
cp /etc/sasldb2 /var/spool/postfix/etc/sasldb2
chown root:sasl /var/spool/postfix/etc/sasldb2 2>/dev/null || true
chmod 640 /var/spool/postfix/etc/sasldb2

adduser postfix sasl 2>/dev/null || true
log_info "SASL 用户 ${SASL_USER_FULL} 已创建于 sasldb（密码已写入）"

postconf -M "submission/inet=submission inet n - y - - smtpd"
postconf -P "submission/inet/syslog_name=postfix/submission"
postconf -P "submission/inet/smtpd_tls_security_level=encrypt"
postconf -P "submission/inet/smtpd_sasl_auth_enable=yes"
postconf -P "submission/inet/smtpd_sasl_security_options=noanonymous"
postconf -P "submission/inet/smtpd_recipient_restrictions=permit_sasl_authenticated,reject"

postconf -M# "smtps/inet" 2>/dev/null || true
postconf -M "465/inet=465 inet n - y - - smtpd"
postconf -P "465/inet/syslog_name=postfix/smtps"
postconf -P "465/inet/smtpd_tls_wrappermode=yes"
postconf -P "465/inet/smtpd_sasl_auth_enable=yes"
postconf -P "465/inet/smtpd_sasl_security_options=noanonymous"
postconf -P "465/inet/smtpd_recipient_restrictions=permit_sasl_authenticated,reject"

postconf -M "2525/inet=2525 inet n - y - - smtpd"
postconf -P "2525/inet/syslog_name=postfix/2525"
postconf -P "2525/inet/smtpd_tls_security_level=encrypt"
postconf -P "2525/inet/smtpd_sasl_auth_enable=yes"
postconf -P "2525/inet/smtpd_sasl_security_options=noanonymous"
postconf -P "2525/inet/smtpd_recipient_restrictions=permit_sasl_authenticated,reject"

log_info "Applying firewall rules and validating SMTP ports"
ensure_smtp_ports_ready

log_info "Using normal Postfix SMTP header handling (Received headers are not filtered)"
restore_normal_postfix_headers
for svc in submission/inet 465/inet 2525/inet; do
  postconf -P "${svc}/local_header_rewrite_clients=permit_inet_interfaces" 2>/dev/null || true
  postconf -P "${svc}/cleanup_service_name=cleanup" 2>/dev/null || true
done

log_info "Applying performance tuning"
postconf -e "default_process_limit = 300"
postconf -e "smtp_destination_concurrency_limit = 50"
postconf -e "smtp_destination_rate_delay = 0"
postconf -e "smtp_extra_recipient_limit = 100"
postconf -e "queue_run_delay = 5s"
postconf -e "minimal_backoff_time = 10s"
postconf -e "maximal_backoff_time = 60s"
postconf -e "maximal_queue_lifetime = 1d"
postconf -e "bounce_queue_lifetime = 6h"
postconf -e "smtp_connection_cache_destinations ="
postconf -e "smtp_connection_cache_on_demand = yes"
postconf -e "smtp_connection_reuse_time_limit = 300s"
postconf -e "smtp_connect_timeout = 10s"
postconf -e "smtp_helo_timeout = 15s"
postconf -e "smtp_mail_timeout = 30s"
postconf -e "smtp_rcpt_timeout = 30s"
postconf -e "smtp_data_init_timeout = 60s"
postconf -e "smtp_data_xfer_timeout = 180s"
postconf -e "smtp_quit_timeout = 10s"
postconf -e "default_destination_concurrency_limit = 50"
postconf -e "local_destination_concurrency_limit = 10"
postconf -e "in_flow_delay = 0s"

log_info "Postfix installed and configured (25/587/465/2525)"

# ============================================================
# Phase 4: 配置 OpenDKIM（DKIM 签名用 RootDomain，与 gcp KumoMTA 路径一致）
# ============================================================
log_info "Configuring OpenDKIM (signing domain=${DOMAIN}, selector=${SELECTOR})"

mkdir -p /etc/opendkim/keys/${DOMAIN}
opendkim-genkey -b 2048 -d ${DOMAIN} -D /etc/opendkim/keys/${DOMAIN} -s ${SELECTOR} -v
chown -R opendkim:opendkim /etc/opendkim/keys/

cat > /etc/opendkim.conf << EOF
AutoRestart             Yes
AutoRestartRate         10/1h
Canonicalization        relaxed/simple
ExternalIgnoreList      refile:/etc/opendkim/TrustedHosts
InternalHosts           refile:/etc/opendkim/TrustedHosts
KeyTable                refile:/etc/opendkim/KeyTable
SigningTable            refile:/etc/opendkim/SigningTable
Mode                    sv
PidFile                 /run/opendkim/opendkim.pid
SignatureAlgorithm      rsa-sha256
UserID                  opendkim:opendkim
Socket                  inet:8891@localhost
EOF

echo "${SELECTOR}._domainkey.${DOMAIN} ${DOMAIN}:${SELECTOR}:/etc/opendkim/keys/${DOMAIN}/${SELECTOR}.private" > /etc/opendkim/KeyTable
echo "*@${DOMAIN} ${SELECTOR}._domainkey.${DOMAIN}" > /etc/opendkim/SigningTable
cat > /etc/opendkim/TrustedHosts << EOF
127.0.0.1
localhost
${FQDN}
${DOMAIN}
*.${DOMAIN}
EOF

mkdir -p /run/opendkim
chown opendkim:opendkim /run/opendkim
systemctl restart opendkim
systemctl enable opendkim
log_info "OpenDKIM installed and configured"

# ============================================================
# Phase 5: 创建邮箱用户
# ============================================================
log_info "Creating mail user: ${MAIL_USER} (nologin, 无系统登录密码)"
# v0.2.9：SMTP AUTH 走 Cyrus SASL sasldb（见 Phase 2 saslpasswd2），本地系统账号仅用于
# Maildir 兜底路径，不需要可登录。改 nologin 且不再 chpasswd（系统账号无密码），缩小攻击面
# ——避免纯发信节点暴露一个可密码登录的系统账号。
if id "${MAIL_USER}" &>/dev/null; then
    log_info "User ${MAIL_USER} already exists, ensuring nologin"
    usermod -s /usr/sbin/nologin "${MAIL_USER}" 2>/dev/null || true
else
    useradd -m -s /usr/sbin/nologin "${MAIL_USER}"
fi
mkdir -p /home/${MAIL_USER}/Maildir
chown -R ${MAIL_USER}:${MAIL_USER} /home/${MAIL_USER}/Maildir
log_info "Mail user created"

# ============================================================
# Phase 6: 输出 DKIM 公钥（与 gcp dkim_setup.sh 协议一致）
# ============================================================
DKIM_TXT="/etc/opendkim/keys/${DOMAIN}/${SELECTOR}.txt"
if [ ! -r "${DKIM_TXT}" ]; then
  log_error "DKIM .txt 不存在: ${DKIM_TXT}"
  exit 1
fi
# 提取 p=... 公钥单行 base64（去 BEGIN/END、引号、括号、空白）
PUBKEY=$(cat "${DKIM_TXT}" | tr -d '\r\n\t "()' | sed -n 's/.*p=\([^;]*\).*/\1/p')
if [ -z "${PUBKEY}" ]; then
  log_error "DKIM 公钥提取为空: ${DKIM_TXT}"
  exit 1
fi
echo "DKIM_PUBLIC_KEY=${PUBKEY}"

# ============================================================
# Phase 7: 最终验证
# ============================================================
systemctl is-active postfix >/dev/null 2>&1 && systemctl is-active opendkim >/dev/null 2>&1 && log_info "=== Verification passed ==="
log_info "=== Full deployment completed ==="
