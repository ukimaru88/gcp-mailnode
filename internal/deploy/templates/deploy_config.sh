#!/bin/bash
# 不用 set -e！任何步骤失败都往 stderr 输出完整诊断
# $1 = init.lua 内容的 base64
# $2 = smtp_auth.lua 内容的 base64

INIT_LUA_B64="$1"
AUTH_LUA_B64="$2"

if [ -z "$INIT_LUA_B64" ] || [ -z "$AUTH_LUA_B64" ]; then
    echo "Usage: $0 <init_lua_b64> <auth_lua_b64>" >&2
    exit 2
fi

# 确保 kumomta 包已装（dpkg 层面，不依赖 PATH，因为 kumod 可能在 /opt/kumomta/sbin/kumod）
if ! dpkg -l kumomta 2>/dev/null | grep -q '^ii '; then
    echo "kumomta 包未装成（dpkg -l kumomta 无 ii 状态）" >&2
    dpkg -l | grep -i kumo >&2 || true
    exit 3
fi

# 找 kumod 可执行路径（PATH、/opt/kumomta/sbin、dpkg 包清单里找）
KUMOD_BIN=""
for candidate in kumod /opt/kumomta/sbin/kumod /usr/sbin/kumod /usr/bin/kumod; do
    if command -v "$candidate" >/dev/null 2>&1; then
        KUMOD_BIN="$candidate"
        break
    elif [ -x "$candidate" ]; then
        KUMOD_BIN="$candidate"
        break
    fi
done
if [ -z "$KUMOD_BIN" ]; then
    # 从包清单里找
    KUMOD_BIN=$(dpkg -L kumomta 2>/dev/null | grep -E '/kumod$' | head -1)
fi
echo "KUMOD_BIN=$KUMOD_BIN (available=$(command -v "$KUMOD_BIN" >/dev/null 2>&1 && echo yes || echo no))"

mkdir -p /opt/kumomta/etc/policy

# 解码 + 写配置
if ! echo "$INIT_LUA_B64" | base64 -d > /opt/kumomta/etc/policy/init.lua 2>/tmp/b64err; then
    echo "base64 解码 init.lua 失败:" >&2
    cat /tmp/b64err >&2
    exit 4
fi
if ! echo "$AUTH_LUA_B64" | base64 -d > /opt/kumomta/etc/policy/smtp_auth.lua 2>/tmp/b64err; then
    echo "base64 解码 smtp_auth.lua 失败:" >&2
    cat /tmp/b64err >&2
    exit 5
fi
chown -R kumod:kumod /opt/kumomta/etc/policy 2>/dev/null || true
chmod 644 /opt/kumomta/etc/policy/init.lua /opt/kumomta/etc/policy/smtp_auth.lua

# 启动
systemctl daemon-reload
systemctl enable kumomta 2>/dev/null || true
systemctl restart kumomta

# 最多等 20 秒
for i in $(seq 1 20); do
    if systemctl is-active kumomta >/dev/null 2>&1; then
        echo "kumomta active after ${i}s"
        ss -tlnp 2>/dev/null | grep -E ':(25|465|587)\b' || true
        exit 0
    fi
    sleep 1
done

# 诊断（stderr）
{
    echo "==================== KUMOMTA FAILED TO START ===================="
    echo "--- systemctl status kumomta ---"
    systemctl status kumomta --no-pager -l 2>&1
    echo ""
    echo "--- journalctl -u kumomta --since '3 minutes ago' ---"
    journalctl -u kumomta --since '3 minutes ago' --no-pager 2>&1
    echo ""
    echo "--- journalctl -u kumomta -n 100 (最后 100 行) ---"
    journalctl -u kumomta -n 100 --no-pager 2>&1
    echo ""
    echo "--- /var/log/kumomta/ ---"
    ls -la /var/log/kumomta/ 2>&1 || echo "(目录不存在)"
    for f in /var/log/kumomta/*.log; do
        [ -f "$f" ] && { echo "--- tail $f ---"; tail -50 "$f"; }
    done 2>&1
    echo ""
    echo "--- init.lua (前 80 行) ---"
    head -80 /opt/kumomta/etc/policy/init.lua 2>&1
    echo ""
    echo "--- kumod --version ---"
    "${KUMOD_BIN:-kumod}" --version 2>&1 || true
    echo ""
    echo "--- dpkg -l | grep kumo ---"
    dpkg -l | grep -i kumo 2>&1 || true
} >&2
exit 1
