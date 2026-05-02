#!/bin/bash
# 多 NIC policy routing：让 KumoMTA bind 到对应 NIC 的内网 IP 时，出公网走对应 NIC
# 参考：https://cloud.google.com/vpc/docs/create-use-multiple-interfaces#configuring_policy_routing
# 由 gcp-mailnode v0.1.57+ 在 install_kumomta.sh 之前下发执行。
# v0.1.65：把规则落到 systemd oneshot，开机自动恢复（重启不丢路由）。
set -euo pipefail

NIC_COUNT={NIC_COUNT}
RUNTIME_SCRIPT=/usr/local/sbin/setup-mailnode-routing.sh
SERVICE_FILE=/etc/systemd/system/setup-mailnode-routing.service

# ----- 0. 最小依赖：curl 必须有（基础镜像如 debian-12-minimal 默认无 curl） -----
if ! command -v curl >/dev/null 2>&1; then
    echo "[setup_policy_routing] curl missing, installing..."
    apt-get update -qq
    apt-get install -y -qq curl ca-certificates
fi

# ----- 1. 写一份 runtime 脚本到 /usr/local/sbin/，systemd 复用 -----
cat > "$RUNTIME_SCRIPT" <<RUNTIME_EOF
#!/bin/bash
# gcp-mailnode runtime: 配置多 NIC policy routing。systemd oneshot 触发，重启自动重跑。
# 幂等：rt_tables 用 grep 去重，ip rule 先 del 再 add。
set -euo pipefail

NIC_COUNT=$NIC_COUNT
META="http://metadata.google.internal/computeMetadata/v1/instance/network-interfaces"

mapfile -t IFACES < <(ip -o link show | awk -F': ' '/^[0-9]+: (ens|eth)/{print \$2}')

if [ "\${#IFACES[@]}" -lt "\$NIC_COUNT" ]; then
    echo "ERROR: 期望 \$NIC_COUNT 个网卡，实际只发现 \${#IFACES[@]}：\${IFACES[*]}" >&2
    exit 1
fi

for i in \$(seq 0 \$((NIC_COUNT-1))); do
    IFACE="\${IFACES[\$i]}"
    IP=\$(curl -sH "Metadata-Flavor: Google" "\$META/\$i/ip")
    GW=\$(curl -sH "Metadata-Flavor: Google" "\$META/\$i/gateway")
    TBL=\$((100+i))

    echo "Configuring NIC \$i: iface=\$IFACE ip=\$IP gw=\$GW table=\$TBL"

    grep -q "^\$TBL nic\$i\$" /etc/iproute2/rt_tables 2>/dev/null \\
        || echo "\$TBL nic\$i" >> /etc/iproute2/rt_tables
    ip route flush table \$TBL 2>/dev/null || true
    ip route add \$GW dev \$IFACE scope link table \$TBL
    ip route add default via \$GW dev \$IFACE table \$TBL

    ip rule del from \$IP 2>/dev/null || true
    ip rule add from \$IP lookup \$TBL pref \$((1000+i))
done

echo "policy routing configured for \$NIC_COUNT NICs"
RUNTIME_EOF
chmod +x "$RUNTIME_SCRIPT"

# ----- 2. 写 systemd unit，开机自动跑 -----
cat > "$SERVICE_FILE" <<UNIT_EOF
[Unit]
Description=gcp-mailnode multi-NIC policy routing
After=network-online.target
Wants=network-online.target
Before=kumomta.service

[Service]
Type=oneshot
ExecStart=$RUNTIME_SCRIPT
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
UNIT_EOF

# ----- 3. 启用 + 立即跑一次 -----
systemctl daemon-reload
systemctl enable setup-mailnode-routing.service >/dev/null
systemctl restart setup-mailnode-routing.service

echo "policy routing service installed and started for $NIC_COUNT NICs"
