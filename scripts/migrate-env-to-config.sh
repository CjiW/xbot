#!/usr/bin/env bash
#
# migrate-env-to-config.sh — 将 .env 文件迁移为 config.json
#
# 用法:
#   ./scripts/migrate-env-to-config.sh [输入文件] [输出文件]
#
# 默认值:
#   输入文件: .env
#   输出文件: $XBOT_HOME/config.json (即 ~/.xbot/config.json)
#
# 如果输出文件已存在，会将 .env 中的值合并进去（.env 优先）。

set -euo pipefail

INPUT="${1:-.env}"
XBOT_HOME="${XBOT_HOME:-$HOME/.xbot}"
OUTPUT="${2:-$XBOT_HOME/config.json}"

# --- 颜色 ---
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

info()  { echo -e "${CYAN}[INFO]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }
ok()    { echo -e "${GREEN}[OK]${NC}    $*"; }

# --- 前置检查 ---
if [[ ! -f "$INPUT" ]]; then
    error "输入文件不存在: $INPUT"
    exit 1
fi

if command -v python3 &>/dev/null; then
    HAS_PYTHON=1
elif command -v jq &>/dev/null; then
    HAS_JQ=1
else
    error "需要 python3 或 jq 来处理 JSON"
    exit 1
fi

# --- 读取 .env 为 key=value 对 ---
declare -A ENV_MAP
while IFS='=' read -r key value; do
    # 跳过注释和空行
    [[ -z "$key" || "$key" =~ ^[[:space:]]*# ]] && continue
    # 去除行内注释（仅未引用的 #）
    value="${value%%#*}"
    # 去除首尾空白
    key=$(echo "$key" | xargs)
    value=$(echo "$value" | xargs)
    ENV_MAP["$key"]="$value"
done < "$INPUT"

if [[ ${#ENV_MAP[@]} -eq 0 ]]; then
    warn ".env 文件中没有有效的配置项"
    exit 0
fi

info "从 $INPUT 读取了 ${#ENV_MAP[@]} 个配置项"

# --- 构建映射表: ENV_KEY -> JSON_PATH ---
# 格式: "ENV_KEY:TYPE:json.path"
# TYPE: s=string, i=int, b=bool, d=duration(秒), f=float, a=comma-array

MAPPINGS=(
    # Server
    "SERVER_HOST:s:server.host"
    "SERVER_PORT:i:server.port"
    "SERVER_READ_TIMEOUT:d:server.read_timeout"
    "SERVER_WRITE_TIMEOUT:d:server.write_timeout"

    # LLM
    "LLM_PROVIDER:s:llm.provider"
    "LLM_BASE_URL:s:llm.base_url"
    "LLM_API_KEY:s:llm.api_key"
    "LLM_MODEL:s:llm.model"
    "LLM_THINKING_MODE:s:llm.thinking_mode"

    # Embedding
    "LLM_EMBEDDING_PROVIDER:s:embedding.provider"
    "LLM_EMBEDDING_BASE_URL:s:embedding.base_url"
    "LLM_EMBEDDING_API_KEY:s:embedding.api_key"
    "LLM_EMBEDDING_MODEL:s:embedding.model"
    "LLM_EMBEDDING_MAX_TOKENS:i:embedding.max_tokens"

    # Agent
    "AGENT_MAX_ITERATIONS:i:agent.max_iterations"
    "AGENT_MAX_CONCURRENCY:i:agent.max_concurrency"
    "MEMORY_PROVIDER:s:agent.memory_provider"
    "WORK_DIR:s:agent.work_dir"
    "PROMPT_FILE:s:agent.prompt_file"
    "AGENT_CONTEXT_MODE:s:agent.context_mode"
    "AGENT_ENABLE_AUTO_COMPRESS:b:agent.enable_auto_compress"
    "AGENT_MAX_CONTEXT_TOKENS:i:agent.max_context_tokens"
    "AGENT_COMPRESSION_THRESHOLD:f:agent.compression_threshold"
    "AGENT_PURGE_OLD_MESSAGES:b:agent.purge_old_messages"
    "MAX_SUBAGENT_DEPTH:i:agent.max_sub_agent_depth"

    # Agent LLM Retry
    "LLM_RETRY_ATTEMPTS:i:agent.llm_retry_attempts"
    "LLM_RETRY_DELAY:d:agent.llm_retry_delay"
    "LLM_RETRY_MAX_DELAY:d:agent.llm_retry_max_delay"
    "LLM_RETRY_TIMEOUT:d:agent.llm_retry_timeout"

    # Agent Timeouts
    "MCP_INACTIVITY_TIMEOUT:d:agent.mcp_inactivity_timeout"
    "MCP_CLEANUP_INTERVAL:d:agent.mcp_cleanup_interval"
    "SESSION_CACHE_TIMEOUT:d:agent.session_cache_timeout"

    # Sandbox
    "SANDBOX_MODE:s:sandbox.mode"
    "SANDBOX_REMOTE_MODE:s:sandbox.remote_mode"
    "SANDBOX_DOCKER_IMAGE:s:sandbox.docker_image"
    "HOST_WORK_DIR:s:sandbox.host_work_dir"
    "SANDBOX_IDLE_TIMEOUT_MINUTES:d:sandbox.idle_timeout"
    "SANDBOX_WS_PORT:i:sandbox.ws_port"
    "SANDBOX_AUTH_TOKEN:s:sandbox.auth_token"
    "SANDBOX_PUBLIC_URL:s:sandbox.public_url"
    "WEB_USER_SERVER_RUNNER:b:sandbox.allow_web_user_server_runner"

    # Feishu
    "FEISHU_ENABLED:b:feishu.enabled"
    "FEISHU_APP_ID:s:feishu.app_id"
    "FEISHU_APP_SECRET:s:feishu.app_secret"
    "FEISHU_ENCRYPT_KEY:s:feishu.encrypt_key"
    "FEISHU_VERIFICATION_TOKEN:s:feishu.verification_token"
    "FEISHU_ALLOW_FROM:a:feishu.allow_from"
    "FEISHU_DOMAIN:s:feishu.domain"

    # QQ
    "QQ_ENABLED:b:qq.enabled"
    "QQ_APP_ID:s:qq.app_id"
    "QQ_CLIENT_SECRET:s:qq.client_secret"
    "QQ_ALLOW_FROM:a:qq.allow_from"

    # NapCat
    "NAPCAT_ENABLED:b:napcat.enabled"
    "NAPCAT_WS_URL:s:napcat.ws_url"
    "NAPCAT_TOKEN:s:napcat.token"
    "NAPCAT_ALLOW_FROM:a:napcat.allow_from"

    # Web
    "WEB_ENABLED:b:web.enabled"
    "WEB_HOST:s:web.host"
    "WEB_PORT:i:web.port"
    "WEB_STATIC_DIR:s:web.static_dir"
    "WEB_UPLOAD_DIR:s:web.upload_dir"
    "WEB_PERSONA_ISOLATION:b:web.persona_isolation"
    "WEB_INVITE_ONLY:b:web.invite_only"

    # OAuth
    "OAUTH_ENABLE:b:oauth.enable"
    "OAUTH_HOST:s:oauth.host"
    "OAUTH_PORT:i:oauth.port"
    "OAUTH_BASE_URL:s:oauth.base_url"

    # Event Webhook
    "EVENT_WEBHOOK_ENABLE:b:event_webhook.enable"
    "EVENT_WEBHOOK_HOST:s:event_webhook.host"
    "EVENT_WEBHOOK_PORT:i:event_webhook.port"
    "EVENT_WEBHOOK_BASE_URL:s:event_webhook.base_url"
    "EVENT_WEBHOOK_MAX_BODY_SIZE:i:event_webhook.max_body_size"
    "EVENT_WEBHOOK_RATE_LIMIT:i:event_webhook.rate_limit"

    # PProf
    "PPROF_ENABLE:b:pprof.enable"
    "PPROF_HOST:s:pprof.host"
    "PPROF_PORT:i:pprof.port"

    # Startup Notify
    "STARTUP_NOTIFY_CHANNEL:s:startup_notify.channel"
    "STARTUP_NOTIFY_CHAT_ID:s:startup_notify.chat_id"

    # Admin
    "ADMIN_CHAT_ID:s:admin.chat_id"

    # Log
    "LOG_LEVEL:s:log.level"
    "LOG_FORMAT:s:log.format"

    # OSS
    "OSS_PROVIDER:s:oss.provider"
    "QINIU_ACCESS_KEY:s:oss.qiniu_access_key"
    "QINIU_SECRET_KEY:s:oss.qiniu_secret_key"
    "QINIU_BUCKET:s:oss.qiniu_bucket"
    "QINIU_DOMAIN:s:oss.qiniu_domain"
    "QINIU_REGION:s:oss.qiniu_region"

    # Misc
    "TAVILY_API_KEY:s:tavily_api_key"
)

# --- 跳过的变量（元配置，不放 config.json）---
SKIP_KEYS=("XBOT_HOME" "XBOT_ENCRYPTION_KEY" "XBOT_TEST_DOCKER")

# --- 构建输出 JSON ---
# 优先用 python3（更可靠），回退到 jq

if [[ ${HAS_PYTHON:-0} -eq 1 ]]; then
    migrate_python() {
        python3 - "$INPUT" "$OUTPUT" << 'PYEOF'
import json, sys, re

input_file = sys.argv[1]
output_file = sys.argv[2]

# Mappings: env_key -> (type, json_path)
MAPPINGS = {
    "SERVER_HOST": ("s", "server.host"),
    "SERVER_PORT": ("i", "server.port"),
    "SERVER_READ_TIMEOUT": ("d", "server.read_timeout"),
    "SERVER_WRITE_TIMEOUT": ("d", "server.write_timeout"),
    "LLM_PROVIDER": ("s", "llm.provider"),
    "LLM_BASE_URL": ("s", "llm.base_url"),
    "LLM_API_KEY": ("s", "llm.api_key"),
    "LLM_MODEL": ("s", "llm.model"),
    "LLM_THINKING_MODE": ("s", "llm.thinking_mode"),
    "LLM_EMBEDDING_PROVIDER": ("s", "embedding.provider"),
    "LLM_EMBEDDING_BASE_URL": ("s", "embedding.base_url"),
    "LLM_EMBEDDING_API_KEY": ("s", "embedding.api_key"),
    "LLM_EMBEDDING_MODEL": ("s", "embedding.model"),
    "LLM_EMBEDDING_MAX_TOKENS": ("i", "embedding.max_tokens"),
    "AGENT_MAX_ITERATIONS": ("i", "agent.max_iterations"),
    "AGENT_MAX_CONCURRENCY": ("i", "agent.max_concurrency"),
    "MEMORY_PROVIDER": ("s", "agent.memory_provider"),
    "WORK_DIR": ("s", "agent.work_dir"),
    "PROMPT_FILE": ("s", "agent.prompt_file"),
    "AGENT_CONTEXT_MODE": ("s", "agent.context_mode"),
    "AGENT_ENABLE_AUTO_COMPRESS": ("b", "agent.enable_auto_compress"),
    "AGENT_MAX_CONTEXT_TOKENS": ("i", "agent.max_context_tokens"),
    "AGENT_COMPRESSION_THRESHOLD": ("f", "agent.compression_threshold"),
    "AGENT_PURGE_OLD_MESSAGES": ("b", "agent.purge_old_messages"),
    "MAX_SUBAGENT_DEPTH": ("i", "agent.max_sub_agent_depth"),
    "LLM_RETRY_ATTEMPTS": ("i", "agent.llm_retry_attempts"),
    "LLM_RETRY_DELAY": ("d", "agent.llm_retry_delay"),
    "LLM_RETRY_MAX_DELAY": ("d", "agent.llm_retry_max_delay"),
    "LLM_RETRY_TIMEOUT": ("d", "agent.llm_retry_timeout"),
    "MCP_INACTIVITY_TIMEOUT": ("d", "agent.mcp_inactivity_timeout"),
    "MCP_CLEANUP_INTERVAL": ("d", "agent.mcp_cleanup_interval"),
    "SESSION_CACHE_TIMEOUT": ("d", "agent.session_cache_timeout"),
    "SANDBOX_MODE": ("s", "sandbox.mode"),
    "SANDBOX_REMOTE_MODE": ("s", "sandbox.remote_mode"),
    "SANDBOX_DOCKER_IMAGE": ("s", "sandbox.docker_image"),
    "HOST_WORK_DIR": ("s", "sandbox.host_work_dir"),
    "SANDBOX_IDLE_TIMEOUT_MINUTES": ("d", "sandbox.idle_timeout"),
    "SANDBOX_WS_PORT": ("i", "sandbox.ws_port"),
    "SANDBOX_AUTH_TOKEN": ("s", "sandbox.auth_token"),
    "SANDBOX_PUBLIC_URL": ("s", "sandbox.public_url"),
    "WEB_USER_SERVER_RUNNER": ("b", "sandbox.allow_web_user_server_runner"),
    "FEISHU_ENABLED": ("b", "feishu.enabled"),
    "FEISHU_APP_ID": ("s", "feishu.app_id"),
    "FEISHU_APP_SECRET": ("s", "feishu.app_secret"),
    "FEISHU_ENCRYPT_KEY": ("s", "feishu.encrypt_key"),
    "FEISHU_VERIFICATION_TOKEN": ("s", "feishu.verification_token"),
    "FEISHU_ALLOW_FROM": ("a", "feishu.allow_from"),
    "FEISHU_DOMAIN": ("s", "feishu.domain"),
    "QQ_ENABLED": ("b", "qq.enabled"),
    "QQ_APP_ID": ("s", "qq.app_id"),
    "QQ_CLIENT_SECRET": ("s", "qq.client_secret"),
    "QQ_ALLOW_FROM": ("a", "qq.allow_from"),
    "NAPCAT_ENABLED": ("b", "napcat.enabled"),
    "NAPCAT_WS_URL": ("s", "napcat.ws_url"),
    "NAPCAT_TOKEN": ("s", "napcat.token"),
    "NAPCAT_ALLOW_FROM": ("a", "napcat.allow_from"),
    "WEB_ENABLED": ("b", "web.enabled"),
    "WEB_HOST": ("s", "web.host"),
    "WEB_PORT": ("i", "web.port"),
    "WEB_STATIC_DIR": ("s", "web.static_dir"),
    "WEB_UPLOAD_DIR": ("s", "web.upload_dir"),
    "WEB_PERSONA_ISOLATION": ("b", "web.persona_isolation"),
    "WEB_INVITE_ONLY": ("b", "web.invite_only"),
    "OAUTH_ENABLE": ("b", "oauth.enable"),
    "OAUTH_HOST": ("s", "oauth.host"),
    "OAUTH_PORT": ("i", "oauth.port"),
    "OAUTH_BASE_URL": ("s", "oauth.base_url"),
    "EVENT_WEBHOOK_ENABLE": ("b", "event_webhook.enable"),
    "EVENT_WEBHOOK_HOST": ("s", "event_webhook.host"),
    "EVENT_WEBHOOK_PORT": ("i", "event_webhook.port"),
    "EVENT_WEBHOOK_BASE_URL": ("s", "event_webhook.base_url"),
    "EVENT_WEBHOOK_MAX_BODY_SIZE": ("i", "event_webhook.max_body_size"),
    "EVENT_WEBHOOK_RATE_LIMIT": ("i", "event_webhook.rate_limit"),
    "PPROF_ENABLE": ("b", "pprof.enable"),
    "PPROF_HOST": ("s", "pprof.host"),
    "PPROF_PORT": ("i", "pprof.port"),
    "STARTUP_NOTIFY_CHANNEL": ("s", "startup_notify.channel"),
    "STARTUP_NOTIFY_CHAT_ID": ("s", "startup_notify.chat_id"),
    "ADMIN_CHAT_ID": ("s", "admin.chat_id"),
    "LOG_LEVEL": ("s", "log.level"),
    "LOG_FORMAT": ("s", "log.format"),
    "OSS_PROVIDER": ("s", "oss.provider"),
    "QINIU_ACCESS_KEY": ("s", "oss.qiniu_access_key"),
    "QINIU_SECRET_KEY": ("s", "oss.qiniu_secret_key"),
    "QINIU_BUCKET": ("s", "oss.qiniu_bucket"),
    "QINIU_DOMAIN": ("s", "oss.qiniu_domain"),
    "QINIU_REGION": ("s", "oss.qiniu_region"),
    "TAVILY_API_KEY": ("s", "tavily_api_key"),
}

SKIP_KEYS = {"XBOT_HOME", "XBOT_ENCRYPTION_KEY", "XBOT_TEST_DOCKER",
             "SINGLE_USER", "AGENT_MEMORY_WINDOW", "AGENT_ENABLE_TOPIC_ISOLATION",
             "AGENT_TOPIC_MIN_SEGMENT_SIZE", "AGENT_TOPIC_SIMILARITY_THRESHOLD",
             "SUBAGENT_LLM_TIMEOUT"}

def parse_duration(s):
    """Parse Go-style duration string to nanoseconds int."""
    s = s.strip()
    if not s:
        return 0
    # Simple parser for common formats: 30s, 5m, 1h, 1h30m
    total_ns = 0
    pattern = re.compile(r'(\d+(?:\.\d+)?)([smh])')
    for match in pattern.finditer(s):
        val = float(match.group(1))
        unit = match.group(2)
        if unit == 's':
            total_ns += val * 1_000_000_000
        elif unit == 'm':
            total_ns += val * 60_000_000_000
        elif unit == 'h':
            total_ns += val * 3_600_000_000_000
    return int(total_ns)

def convert_value(raw, typ):
    if typ == 's':
        return raw
    elif typ == 'i':
        return int(raw)
    elif typ == 'b':
        return raw.lower() in ('true', '1', 'yes')
    elif typ == 'f':
        return float(raw)
    elif typ == 'd':
        return parse_duration(raw)
    elif typ == 'a':
        return [x.strip() for x in raw.split(',') if x.strip()]
    return raw

def set_nested(obj, path, value):
    parts = path.split('.')
    for p in parts[:-1]:
        if p not in obj or not isinstance(obj[p], dict):
            obj[p] = {}
        obj = obj[p]
    obj[parts[-1]] = value

# Parse .env
env = {}
with open(input_file) as f:
    for line in f:
        line = line.strip()
        if not line or line.startswith('#'):
            continue
        if '=' not in line:
            continue
        key, _, value = line.partition('=')
        # Strip inline comments (naive: only if not inside quotes)
        comment_pos = value.find('#')
        if comment_pos > 0 and value[comment_pos-1] != '"':
            value = value[:comment_pos]
        key = key.strip()
        value = value.strip()
        if key and not key.startswith('#'):
            env[key] = value

# Load existing config.json (if any)
import os
try:
    with open(output_file) as f:
        config = json.load(f)
except (FileNotFoundError, json.JSONDecodeError):
    config = {}

# Apply mappings
migrated = 0
skipped_meta = []
skipped_unknown = []
for key, value in env.items():
    if key in SKIP_KEYS:
        skipped_meta.append(key)
        continue
    if key not in MAPPINGS:
        skipped_unknown.append(key)
        continue
    typ, path = MAPPINGS[key]
    converted = convert_value(value, typ)
    set_nested(config, path, converted)
    migrated += 1

# Write output
os.makedirs(os.path.dirname(output_file) or '.', exist_ok=True)
with open(output_file, 'w') as f:
    json.dump(config, f, indent=2, ensure_ascii=False)
    f.write('\n')

print(f"migrated={migrated}")
if skipped_meta:
    print(f"skipped_meta={','.join(skipped_meta)}")
if skipped_unknown:
    print(f"skipped_unknown={','.join(skipped_unknown)}")
PYEOF
    }

    RESULT=$(migrate_python)

    MIGRATED=$(echo "$RESULT" | grep '^migrated=' | cut -d= -f2)
    SKIPPED_META=$(echo "$RESULT" | grep '^skipped_meta=' | cut -d= -f2-)
    SKIPPED_UNKNOWN=$(echo "$RESULT" | grep '^skipped_unknown=' | cut -d= -f2-)

else
    # Fallback: pure bash + jq
    migrate_jq() {
        local jq_expr='{}'

        for mapping in "${MAPPINGS[@]}"; do
            IFS=':' read -r typ json_path <<< "$mapping"
            # 从 json_path 反推 env key (server.host -> SERVER_HOST)
            local env_key=$(echo "$json_path" | awk -F. '{
                for(i=1;i<=NF;i++) {
                    $i = toupper($i)
                }
            }' | tr '.' '_')

            # 查找特殊映射
            case "$json_path" in
                sandbox.allow_web_user_server_runner) env_key="WEB_USER_SERVER_RUNNER" ;;
                agent.llm_retry_*) env_key="LLM_RETRY_$(echo $json_path | awk -F. '{print $NF}' | tr '[:lower:]' '[:upper:]')" ;;
                agent.mcp_inactivity_timeout) env_key="MCP_INACTIVITY_TIMEOUT" ;;
                agent.mcp_cleanup_interval) env_key="MCP_CLEANUP_INTERVAL" ;;
                agent.session_cache_timeout) env_key="SESSION_CACHE_TIMEOUT" ;;
                oss.qiniu_*) env_key="QINIU_$(echo $json_path | awk -F. '{print toupper($2)}' | tr '[:lower:]' '[:upper:]')" ;;
            esac

            if [[ -v "ENV_MAP[$env_key]" && -n "${ENV_MAP[$env_key]}" ]]; then
                local val="${ENV_MAP[$env_key]}"
                local jq_val

                case "$typ" in
                    s) jq_val=$(jq -n --arg v "$val" '$v') ;;
                    i) jq_val="$val" ;;
                    b) jq_val="$([[ "$val" =~ ^(true|1|yes)$ ]] && echo 'true' || echo 'false')" ;;
                    f) jq_val="$val" ;;
                    d)
                        # 简单解析: 纯数字视为秒
                        if [[ "$val" =~ ^[0-9]+$ ]]; then
                            jq_val="$(( val * 1000000000 ))"
                        elif [[ "$val" =~ ^([0-9]+)([smh])$ ]]; then
                            local num="${BASH_REMATCH[1]}"
                            local unit="${BASH_REMATCH[2]}"
                            case "$unit" in
                                s) jq_val="$(( num * 1000000000 ))" ;;
                                m) jq_val="$(( num * 60000000000 ))" ;;
                                h) jq_val="$(( num * 3600000000000 ))" ;;
                            esac
                        else
                            jq_val="0"
                        fi
                        ;;
                    a) jq_val=$(jq -n --arg v "$val" '$v | split(",") | map(select(length > 0))') ;;
                esac

                jq_expr="$jq_expr | .${json_path} = ${jq_val}"
                MIGRATED=$((MIGRATED + 1))
            fi
        done

        # Merge with existing config if present
        if [[ -f "$OUTPUT" ]]; then
            jq_expr="input | $jq_expr * ."
        fi

        echo "$jq_expr"
    }

    # Build jq expression and apply
    JQ_EXPR=$(migrate_jq)
    if [[ -f "$OUTPUT" ]]; then
        jq "$JQ_EXPR" "$OUTPUT" > "$OUTPUT.tmp"
    else
        jq -n "$JQ_EXPR" > "$OUTPUT.tmp"
    fi
    mv "$OUTPUT.tmp" "$OUTPUT"
fi

# --- 结果 ---
echo ""
ok "迁移完成!"
echo ""
echo "  输出文件: $OUTPUT"
echo "  迁移项数: ${MIGRATED:-0}"

if [[ -n "${SKIPPED_META:-}" ]]; then
    warn "跳过元配置变量（保留为环境变量）: $SKIPPED_META"
fi

if [[ -n "${SKIPPED_UNKNOWN:-}" ]]; then
    warn "跳过未知/废弃变量: $SKIPPED_UNKNOWN"
fi

echo ""
info "请检查生成的 config.json，确认无误后可删除 .env 文件"
info "如需回退: mv $OUTPUT ${OUTPUT}.bak"
