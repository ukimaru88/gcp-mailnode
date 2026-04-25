#!/bin/bash
set -e
DOMAIN="{DOMAIN}"
SELECTOR="{SELECTOR}"
KEY_DIR="/opt/kumomta/etc/keys/$DOMAIN"
KEY_FILE="$KEY_DIR/$SELECTOR.key"
PUB_FILE="$KEY_DIR/$SELECTOR.pub"

mkdir -p "$KEY_DIR"

if [ ! -f "$KEY_FILE" ]; then
    openssl genrsa -out "$KEY_FILE" 2048 2>/dev/null
    openssl rsa -in "$KEY_FILE" -pubout -out "$PUB_FILE" 2>/dev/null
fi

# 兜底：apt 装 kumomta 包时 /opt/kumomta/etc/keys 默认是 kumod:kumod 700，
# root SSH 写入子文件后 owner=root、kumod 可能读不到。这里强制 chown + chmod，
# 不再用 "|| true" 吞错——本来就该 root 在 own 这一棵树有权操作。
chown -R kumod:kumod /opt/kumomta/etc/keys
chmod 700 "$KEY_DIR"
chmod 600 "$KEY_FILE"
chmod 644 "$PUB_FILE"

# 自验证：失败立刻 exit 1，避免下游 self-check 拿模糊错误
if [ ! -r "$KEY_FILE" ]; then
    echo "FATAL: $KEY_FILE 不可读（root 视角）" >&2
    ls -la "$KEY_DIR" >&2
    exit 1
fi
if [ ! -r "$PUB_FILE" ]; then
    echo "FATAL: $PUB_FILE 不可读（root 视角）" >&2
    ls -la "$KEY_DIR" >&2
    exit 1
fi

# 诊断信息（写到 stdout，便于失败时贴出来）
echo "DKIM_KEY_FILE=$KEY_FILE"
ls -la "$KEY_DIR"

# 提取公钥 base64 单行（去除 BEGIN/END 行和换行），输出 DKIM_PUBLIC_KEY=... 供 Go 捕获
PUBKEY=$(grep -v 'PUBLIC KEY' "$PUB_FILE" | tr -d '\n\r ')
if [ -z "$PUBKEY" ]; then
    echo "FATAL: DKIM 公钥提取为空" >&2
    echo "--- $PUB_FILE 全文 ---" >&2
    cat "$PUB_FILE" >&2
    exit 1
fi
echo "DKIM_PUBLIC_KEY=${PUBKEY}"
