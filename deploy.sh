#!/usr/bin/env bash
# deploy.sh — 一条命令把 remote-claude 部署到一台 VPS（在 Mac 上跑）
#
#   ./deploy.sh                    .env 里有 DEPLOY_TARGET 就按它一键部署，全程不确认；
#                                  没有就交互式地问
#   ./deploy.sh --no-env           不用 .env：交互式输入部署信息，每一步都确认
#   ./deploy.sh root@1.2.3.4:22    直接给目标（优先于 .env，逐步确认；加 -y 就不确认）
#   ./deploy.sh --check            只检测两端环境，不装不改
#
#   -y, --yes        不确认、不提问，缺的用默认值（按 .env 部署时自动就是这样）
#   --no-env         不读 .env，也不看已 export 的 DEPLOY_* 变量
#   --env FILE       部署信息文件（默认仓库根目录的 .env，模板见 .env.example）
#   --alias NAME     ~/.ssh/config 里的 Host 别名（默认沿用已指向这台机器的，没有就新建）
#   --key PATH       新建 Host 块时用的私钥（默认 ~/.ssh/id_ed25519，不存在就生成）
#   --no-web         不部署网页端 rcweb
#   --no-public      网页端只监听 127.0.0.1（走 rcweb tunnel / Tailscale），不开到公网 IP
#
# 一键模式下唯一可能要你动手的：公钥还没装到 VPS 时输一次 SSH 登录密码。
#
# 每一步都是先检测、缺了才装、已经对了就跳过，所以可以反复跑：
#   1. 本机：mutagen / mosh / tmux，部署网页端时再加 go / node（用 Homebrew 装）
#   2. SSH：生成密钥、写 ~/.ssh/config 的 Host 块、把公钥装到 VPS（只有这一步要输一次密码）
#   3. 本机配置：RC_HOST 指向这台机器，再跑 install.sh 装命令
#   4. VPS：bash / curl / git / tmux / mosh、Claude Code、mosh 要的 UTF-8 locale 和防火墙
#   5. 网页端：rcweb deploy（登录密码没设就问你，或自动生成），默认开在 http://<VPS IP>:7681

set -uo pipefail

REPO="$(cd "$(dirname "$0")" && pwd)"
RC_HOME="${RC_HOME:-${XDG_CONFIG_HOME:-$HOME/.config}/remote-claude}"
CFG="$RC_HOME/config"
SSH_CFG="$HOME/.ssh/config"
WEB_ENV="$REPO/server/.env"
ENV_FILE="$REPO/.env"

ok()    { printf '  \033[32m✓\033[0m %s\n' "$*"; }
warn()  { printf '  \033[33m!\033[0m %s\n' "$*"; }
bad()   { printf '  \033[31m✗\033[0m %s\n' "$*"; }
info()  { printf '  \033[2m%s\033[0m\n' "$*"; }
head_() { printf '\n\033[1m%s\033[0m\n' "$*"; }
die()   { printf '\ndeploy: %s\n' "$*" >&2; exit 1; }

# 打印文件头那段注释（第一行 shebang 之后、第一行代码之前）
usage() { awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit "${1:-0}"; }

YES=0 CHECK=0 NO_ENV=0 ARG_ENV=0
ARG_TARGET="" ARG_ALIAS="" ARG_KEY="" ARG_WEB="" ARG_PUBLIC=""
while [ $# -gt 0 ]; do
    case "$1" in
        -y|--yes)  YES=1; shift ;;
        --check)   CHECK=1; shift ;;
        --no-env)  NO_ENV=1; shift ;;
        --env)     [ $# -ge 2 ] || die "--env 需要参数"; ENV_FILE="$2"; ARG_ENV=1; shift 2 ;;
        --alias)   [ $# -ge 2 ] || die "--alias 需要参数"; ARG_ALIAS="$2"; shift 2 ;;
        --key)     [ $# -ge 2 ] || die "--key 需要参数"; ARG_KEY="$2"; shift 2 ;;
        --no-web)  ARG_WEB=0; shift ;;
        --no-public) ARG_PUBLIC=0; shift ;;
        -h|--help) usage 0 ;;
        -*)        die "未知参数 ${1}（--help 看用法）" ;;
        *)         [ -z "$ARG_TARGET" ] || die "只能给一个目标"; ARG_TARGET="$1"; shift ;;
    esac
done
[ "$NO_ENV" = 1 ] && [ "$ARG_ENV" = 1 ] && die "--env 和 --no-env 不能一起用"

TTY=0; [ -t 0 ] && [ -t 1 ] && TTY=1
TS="$(date +%Y%m%d%H%M%S)"
FAILED=()
WEB_URL=""

have() { command -v "$1" >/dev/null 2>&1; }
interactive() { [ "$TTY" = 1 ] && [ "$YES" != 1 ]; }

# 提示 [默认值] → 回答（提示走 stderr，回答走 stdout，方便 $(...) 接）
ask() {
    local a
    printf '  %s%s: ' "$1" "${2:+ [$2]}" >&2
    IFS= read -r a || a=""
    printf '%s' "${a:-${2:-}}"
}

# 直接回车算"是"；-y 直接过；不是终端又没 -y 就停下，不替人拿主意。
# 读到 EOF（输入被关掉）算"否"：没人回答不能当成同意
confirm() {
    [ "$YES" = 1 ] && return 0
    [ "$TTY" = 1 ] || die "要确认「$1」，但当前不是终端；非交互运行请加 -y"
    local a
    printf '  %s [Y/n] ' "$1"
    IFS= read -r a || { echo; return 1; }
    case "$a" in ""|y|Y|yes|YES) return 0 ;; *) return 1 ;; esac
}

# .env 里某个键的值：最后一次赋值为准，去掉行尾注释和两侧引号
envval() {
    [ -f "$1" ] || return 0
    sed -n "s/^[[:space:]]*\(export[[:space:]]\{1,\}\)\{0,1\}$2=//p" "$1" | tail -n 1 |
        sed -e 's/[[:space:]]\{1,\}#.*$//' -e 's/[[:space:]]*$//' \
            -e 's/^"\(.*\)"$/\1/' -e "s/^'\(.*\)'\$/\1/"
}

# 把文件里第一处 KEY= 改成 KEY=VALUE，没有就追加。值经 ENVIRON 传给 awk，不怕特殊字符
set_kv() {
    local f="$1" tmp
    tmp="$(mktemp "$f.XXXXXX")" || return 1
    K="$2" V="$3" awk '
        BEGIN { k = ENVIRON["K"]; v = ENVIRON["V"] }
        !done && $0 ~ "^[ \t]*(export[ \t]+)?" k "=" { print k "=" v; done = 1; next }
        { print }
        END { if (!done) print k "=" v }
    ' "$f" > "$tmp" && cat "$tmp" > "$f"   # cat 回去而不是 mv：保留原文件的权限
    local rc=$?
    rm -f "$tmp"
    return $rc
}

# config 是 shell 脚本，取值就在子 shell 里 source 一遍
cfg_get() {
    [ -f "$CFG" ] || return 0
    ( set +u; . "$CFG" >/dev/null 2>&1; eval "printf '%s' \"\${$1:-}\"" )
}

expand() {
    case "$1" in
        "~")   printf '%s' "$HOME" ;;
        "~/"*) printf '%s/%s' "$HOME" "${1#\~/}" ;;
        *)     printf '%s' "$1" ;;
    esac
}
tildify() {
    case "$1" in
        "$HOME"/*) printf '~/%s' "${1#"$HOME"/}" ;;
        *)         printf '%s' "$1" ;;
    esac
}
lower() { printf '%s' "$1" | tr 'A-Z' 'a-z'; }

ssh_() { ssh -o BatchMode=yes -o ConnectTimeout=15 "$ALIAS" "$@"; }

# ---- 部署目标 ----

# [用户@]主机[:端口]，也认 [IPv6]:端口
parse_target() {
    local t="$1" hp
    case "$t" in
        *@*) T_USER="${t%%@*}"; hp="${t#*@}" ;;
        *)   T_USER=root; hp="$t" ;;
    esac
    case "$hp" in
        \[*\]:*) T_HOST="${hp%%]*}"; T_HOST="${T_HOST#\[}"; T_PORT="${hp##*]:}" ;;
        \[*\])   T_HOST="${hp#\[}"; T_HOST="${T_HOST%]}"; T_PORT=22 ;;
        *:*:*)   T_HOST="$hp"; T_PORT=22 ;;
        *:*)     T_HOST="${hp%:*}"; T_PORT="${hp##*:}" ;;
        *)       T_HOST="$hp"; T_PORT=22 ;;
    esac
    [[ "$T_USER" =~ ^[A-Za-z0-9._-]+$ ]] || return 1
    [[ "$T_HOST" =~ ^[A-Za-z0-9.:-]+$ ]] || return 1
    [[ "$T_PORT" =~ ^[0-9]+$ ]] && [ "$T_PORT" -ge 1 ] && [ "$T_PORT" -le 65535 ] || return 1
    case "$T_HOST" in
        *:*) T_DEST="$T_USER@[$T_HOST]:$T_PORT" ;;
        *)   T_DEST="$T_USER@$T_HOST:$T_PORT" ;;
    esac
}

# ssh -G 的某一项（ssh 自己解析 ~/.ssh/config，含 Include 和默认值）
ssh_opt() { ssh -G "$1" 2>/dev/null | awk -v k="$2" '$1==k { print $2; exit }'; }
ssh_dest() { printf '%s@%s:%s' "$(ssh_opt "$1" user)" "$(ssh_opt "$1" hostname)" "$(ssh_opt "$1" port)"; }

# ~/.ssh/config 里写明的 Host 名（不含通配）
ssh_hosts() {
    [ -f "$SSH_CFG" ] || return 0
    awk 'tolower($1)=="host" { for (i = 2; i <= NF; i++) if ($i !~ /[*?!]/) print $i }' "$SSH_CFG"
}

# 未定义的 Host 会被 ssh -G 原样回显成 hostname（install.sh 同一个判断）
alias_defined() {
    ssh_hosts | grep -qxF "$1" && return 0
    [ "$(lower "$(ssh_opt "$1" hostname)")" != "$(lower "$1")" ]
}
points_to_target() {
    ssh -G "$1" 2>/dev/null | awk -v h="$(lower "$T_HOST")" -v u="$T_USER" -v p="$T_PORT" '
        $1=="hostname" { hh = tolower($2) }
        $1=="user"     { uu = $2 }
        $1=="port"     { pp = $2 }
        END { exit !(hh == h && uu == u && pp == p) }'
}
# 只为端口转发写的块（比如远程桌面隧道）不拿来当主连接
forward_only() { ssh -G "$1" 2>/dev/null | grep -qiE '^(localforward|remoteforward|dynamicforward) '; }

default_alias() {
    local base a n=1
    case "$T_HOST" in
        *:*)       base=vps ;;
        *[!0-9.]*) base="$(printf '%s' "${T_HOST%%.*}" | tr -c 'A-Za-z0-9-' '-')" ;;
        *)         base="vps-${T_HOST%%.*}" ;;
    esac
    a="$base"
    while alias_defined "$a"; do n=$((n + 1)); a="$base-$n"; done
    printf '%s' "$a"
}

pick_alias() {
    local c
    if [ -z "$ALIAS" ]; then
        # 优先沿用 config 里现有的 RC_HOST，其次 ~/.ssh/config 里任何一个指向同一台的
        for c in $(cfg_get RC_HOST) $(ssh_hosts); do
            if points_to_target "$c" && ! forward_only "$c"; then ALIAS="$c"; break; fi
        done
    fi
    if [ -z "$ALIAS" ]; then
        ALIAS="$(default_alias)"
        interactive && ALIAS="$(ask 'SSH 别名（写进 ~/.ssh/config 的 Host 名）' "$ALIAS")"
    fi
    [[ "$ALIAS" =~ ^[A-Za-z0-9._-]+$ ]] || die "别名只能用字母、数字和 . _ -：$ALIAS"
    if alias_defined "$ALIAS"; then
        points_to_target "$ALIAS" ||
            die "~/.ssh/config 里的 Host $ALIAS 指向 $(ssh_dest "$ALIAS")，不是 ${T_DEST}；换个别名（--alias），或改那个 Host 块"
        ALIAS_NEW=0
    else
        ALIAS_NEW=1
    fi
}

# 已有 Host 块就用它配的密钥，保证装到 VPS 上的公钥正是 ssh 会拿去用的那把
pick_key() {
    local f
    if [ "$ALIAS_NEW" = 0 ]; then
        for f in $(ssh -G "$ALIAS" 2>/dev/null | awk '$1=="identityfile" { print $2 }'); do
            f="$(expand "$f")"
            [ -f "$f" ] && { KEY="$f"; return; }
        done
    fi
    KEY="$(expand "${KEY:-$HOME/.ssh/id_ed25519}")"
}

# 部署信息的一项：已 export 的 DEPLOY_* 优先，其次 .env；--no-env 时两样都不看
dval() {
    [ "$NO_ENV" = 1 ] && return 0
    local v
    eval "v=\"\${$1:-}\""
    if [ -n "$v" ]; then printf '%s' "$v"; else envval "$ENV_FILE" "$1"; fi
}

resolve_info() {
    head_ "部署信息"
    local src
    TARGET="$ARG_TARGET" src="命令行参数"
    if [ -z "$TARGET" ]; then
        TARGET="$(dval DEPLOY_TARGET)"
        if [ -n "$TARGET" ]; then
            if [ -n "${DEPLOY_TARGET:-}" ]; then src="环境变量 DEPLOY_TARGET"; else src="$(tildify "$ENV_FILE")"; fi
            # 目标来自 .env = 一键部署：信息已经写好了，后面不再确认、不再提问
            YES=1
        fi
    fi
    ALIAS="${ARG_ALIAS:-$(dval DEPLOY_ALIAS)}"
    KEY="${ARG_KEY:-$(dval DEPLOY_KEY)}"
    WEB="${ARG_WEB:-$(dval DEPLOY_WEB)}"
    PUBLIC="${ARG_PUBLIC:-$(dval DEPLOY_WEB_PUBLIC)}"

    if [ -z "$TARGET" ]; then
        if ! interactive; then
            [ "$NO_ENV" = 1 ] && die "--no-env 要在终端里交互式输入；非交互运行请把目标作为参数传入"
            die "没有部署目标：在 $(tildify "$ENV_FILE") 里写 DEPLOY_TARGET=root@1.2.3.4:22，或作为参数传入"
        fi
        TARGET="$(ask '部署目标 用户@主机[:端口]')"
        [ -n "$TARGET" ] || die "没有部署目标"
        src="交互输入"
    fi
    parse_target "$TARGET" || die "看不懂的目标：${TARGET}（格式 用户@主机[:端口]，如 root@1.2.3.4:22）"
    pick_alias
    pick_key
    if [ -z "$WEB" ]; then
        WEB=1
        if interactive && [ "$CHECK" != 1 ]; then confirm "部署网页端 rcweb？" || WEB=0; fi
    fi
    if [ "$WEB" = 1 ] && [ -z "$PUBLIC" ]; then
        PUBLIC=1
        if interactive && [ "$CHECK" != 1 ]; then
            confirm "网页端直接用 $(web_url) 打开？（明文 HTTP；选 n 则只监听 127.0.0.1，走隧道 / Tailscale）" || PUBLIC=0
        fi
    fi

    printf '  目标      %s（来自 %s）\n' "$T_DEST" "$src"
    printf '  SSH 别名  %s（%s）\n' "$ALIAS" "$([ "$ALIAS_NEW" = 1 ] && echo '将新建' || echo '~/.ssh/config 已有')"
    printf '  密钥      %s（%s）\n' "$(tildify "$KEY")" "$([ -f "$KEY" ] && echo '已有' || echo '将生成')"
    if [ "$WEB" != 1 ]; then
        printf '  网页端    不部署\n'
    elif [ "$PUBLIC" = 1 ]; then
        printf '  网页端    部署 rcweb，公网直连 %s（不想开到公网加 --no-public）\n' "$(web_url)"
    else
        printf '  网页端    部署 rcweb，只监听 127.0.0.1（rcweb tunnel / Tailscale 访问）\n'
    fi

    [ "$CHECK" = 1 ] && return 0
    [ "$YES" = 1 ] && info "一键部署，不再逐步确认$([ "$src" = 命令行参数 ] || echo '（要逐步确认就加 --no-env）')"
    if [ "$src" = 交互输入 ] && [ "$NO_ENV" != 1 ] &&
        confirm "记到 $(tildify "$ENV_FILE")，下次直接一键部署？"; then
        [ -f "$ENV_FILE" ] || cp "$REPO/.env.example" "$ENV_FILE" 2>/dev/null || : > "$ENV_FILE"
        chmod 600 "$ENV_FILE"
        set_kv "$ENV_FILE" DEPLOY_TARGET "$TARGET" && set_kv "$ENV_FILE" DEPLOY_WEB "$WEB" &&
            { [ -z "$PUBLIC" ] || set_kv "$ENV_FILE" DEPLOY_WEB_PUBLIC "$PUBLIC"; } &&
            ok "已写入 $(tildify "$ENV_FILE")"
    fi
    interactive && { confirm "开始部署？" || die "已取消"; }
    return 0
}

# ---- 本机环境 ----

# 版本号 → 主、次两段（go1.25.6 / v22.3.0 都认）
ver_ge() {
    local v="$1" ma mi
    v="${v#go}"; v="${v#v}"
    ma="${v%%.*}"; mi="${v#*.}"; mi="${mi%%[!0-9]*}"
    [[ "$ma" =~ ^[0-9]+$ ]] && [[ "$mi" =~ ^[0-9]+$ ]] || return 1
    [ "$ma" -gt "$2" ] || { [ "$ma" -eq "$2" ] && [ "$mi" -ge "$3" ]; }
}
# go.mod 要 1.24；1.21 起 go 会自己下载所需的工具链
go_ok() { ver_ge "$(go env GOVERSION 2>/dev/null)" 1 21; }
# vite 8 要 ^20.19 || >=22.12
node_ok() {
    local v; v="$(node -v 2>/dev/null)" || return 1
    ver_ge "$v" 22 12 || { ver_ge "$v" 20 19 && ! ver_ge "$v" 21 0; }
}
local_ok() {
    case "$1" in
        go)   go_ok ;;
        node) node_ok && have npm ;;
        *)    have "$1" ;;
    esac
}
local_desc() {
    case "$1" in
        go)   printf 'go %s' "$(go env GOVERSION 2>/dev/null | sed 's/^go//')" ;;
        node) printf 'node %s' "$(node -v 2>/dev/null | sed 's/^v//')" ;;
        *)    printf '%s' "$1" ;;
    esac
}

ensure_brew() {
    have brew && return 0
    local b
    for b in /opt/homebrew/bin/brew /usr/local/bin/brew /home/linuxbrew/.linuxbrew/bin/brew; do
        [ -x "$b" ] && { eval "$("$b" shellenv)"; return 0; }
    done
    [ "$(uname -s)" = Darwin ] || return 1
    confirm "没有 Homebrew，现在装？（官方脚本，过程中要输 sudo 密码）" || return 1
    [ "$YES" = 1 ] && export NONINTERACTIVE=1
    /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)" || return 1
    for b in /opt/homebrew/bin/brew /usr/local/bin/brew; do
        if [ -x "$b" ]; then
            eval "$("$b" shellenv)"
            warn "新开的终端要用 brew 装的命令，把这行加进 ~/.zprofile：eval \"\$($b shellenv)\""
            return 0
        fi
    done
    return 1
}

# 命令:Homebrew 包:缺了算不算失败（mosh 缺了还能 --ssh，tmux 本机只是体检项）
LOCAL_SPECS=("mutagen:mutagen-io/mutagen/mutagen:1" "mosh:mosh:0" "tmux:tmux:0")

# 打印一遍状态，缺的包放进 MISSING，其中必需的放进 MISSING_REQ
local_scan() {
    local s c p
    MISSING=() MISSING_REQ=()
    for s in "${LOCAL_SPECS[@]}"; do
        c="${s%%:*}"; p="${s#*:}"; p="${p%:*}"
        if local_ok "$c"; then ok "$(local_desc "$c")"; continue; fi
        MISSING+=("$p")
        if [ "${s##*:}" = 1 ]; then
            MISSING_REQ+=("本机 $c")
            if have "$c"; then bad "$(local_desc "$c") 版本不够（$(command -v "$c")）"; else bad "$c 缺失"; fi
        else
            warn "$c 缺失"
        fi
    done
}

local_env() {
    head_ "本机环境"
    have ssh || die "本机没有 ssh"
    ok "ssh"
    [ "$WEB" = 1 ] && LOCAL_SPECS+=("go:go:1" "node:node:1")
    local_scan
    [ ${#MISSING[@]} -gt 0 ] || return 0
    if [ "$CHECK" != 1 ]; then
        if ! ensure_brew; then
            warn "没有 Homebrew，手动装一下：${MISSING[*]}"
        elif confirm "用 Homebrew 安装 ${MISSING[*]}？"; then
            local p
            for p in "${MISSING[@]}"; do
                if brew list --formula "$p" >/dev/null 2>&1; then brew upgrade "$p"; else brew install "$p"; fi ||
                    warn "brew 装 $p 失败"
            done
            hash -r
            info "装完再查一遍"
            local_scan
        fi
    fi
    [ ${#MISSING_REQ[@]} -gt 0 ] && FAILED+=("${MISSING_REQ[@]}")
    return 0
}

# ---- SSH ----

write_host_block() {
    mkdir -p "$HOME/.ssh" && chmod 700 "$HOME/.ssh"
    if [ -f "$SSH_CFG" ]; then
        cp "$SSH_CFG" "$SSH_CFG.bak.$TS" && info "原 ~/.ssh/config 备份为 config.bak.$TS"
    fi
    {
        [ -s "$SSH_CFG" ] && [ -n "$(tail -c 1 "$SSH_CFG")" ] && echo
        printf '\n# remote-claude（deploy.sh 添加于 %s）\n' "$(date '+%F')"
        # 取推荐 Host 块的正文，去掉说明性注释，填上这台机器的信息
        sed -n '/^Host /,$p' "$REPO/config/ssh.config.snippet" | grep -v '^[[:space:]]*#' | sed \
            -e "s|^Host .*|Host $ALIAS|" \
            -e "s|^\([[:space:]]*\)HostName .*|\1HostName $T_HOST|" \
            -e "s|^\([[:space:]]*\)User .*|\1User $T_USER|" \
            -e "s|^\([[:space:]]*\)Port .*|\1Port $T_PORT|" \
            -e "s|^\([[:space:]]*\)IdentityFile .*|\1IdentityFile $(tildify "$KEY")|"
    } >> "$SSH_CFG"
    chmod 600 "$SSH_CFG"
}

# 用密码登录一次，把公钥追加进 authorized_keys。命令不含单引号，整体交给远端的 sh，
# 这样对方登录 shell 是 fish / zsh 也不受影响
AUTH_CMD='umask 077; mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && k="$(cat)" && [ -n "$k" ] || exit 1; grep -qxF "$k" ~/.ssh/authorized_keys && exit 0; if [ -s ~/.ssh/authorized_keys ] && [ -n "$(tail -c 1 ~/.ssh/authorized_keys)" ]; then echo >> ~/.ssh/authorized_keys; fi; printf "%s\n" "$k" >> ~/.ssh/authorized_keys; if command -v restorecon >/dev/null 2>&1; then restorecon -R ~/.ssh; fi; exit 0'

authorize_key() {
    info "把 $(tildify "$KEY").pub 装到 ${T_DEST}，请输入 $T_USER 的登录密码"
    ssh -o ControlMaster=no -o ControlPath=none -o PubkeyAuthentication=no \
        -o PreferredAuthentications=keyboard-interactive,password -o NumberOfPasswordPrompts=3 \
        -o StrictHostKeyChecking=accept-new -o ConnectTimeout=20 \
        "$ALIAS" "sh -c '$AUTH_CMD'" < "$KEY.pub"
}

setup_ssh() {
    head_ "SSH → $ALIAS"
    mkdir -p "$HOME/.ssh/cm" && chmod 700 "$HOME/.ssh/cm"

    if [ -f "$KEY" ]; then
        ok "密钥 $(tildify "$KEY")"
    elif [ "$CHECK" = 1 ]; then
        bad "密钥 $(tildify "$KEY") 不存在（部署时生成）"
    else
        mkdir -p "$(dirname "$KEY")" && chmod 700 "$(dirname "$KEY")"
        ssh-keygen -q -t ed25519 -N "" -C "remote-claude@$(hostname -s)" -f "$KEY" || die "生成密钥失败"
        ok "生成密钥 $(tildify "$KEY")（ed25519，无口令）"
    fi
    if [ -f "$KEY" ] && [ ! -f "$KEY.pub" ] && [ "$CHECK" != 1 ]; then
        ssh-keygen -y -f "$KEY" > "$KEY.pub" || die "从 $(tildify "$KEY") 导出公钥失败"
    fi

    if [ "$ALIAS_NEW" = 0 ]; then
        ok "~/.ssh/config 已有 Host $ALIAS → $T_DEST"
    elif [ "$CHECK" = 1 ]; then
        bad "~/.ssh/config 里还没有指向 $T_DEST 的 Host（部署时新建 ${ALIAS}）"
        return 1
    else
        write_host_block && ok "~/.ssh/config 新增 Host $ALIAS → $T_DEST"
    fi

    # accept-new：第一次连接自动记下主机指纹，但指纹变了照样拒绝
    local err rc
    err="$(mktemp)"
    ssh -o BatchMode=yes -o ConnectTimeout=15 -o StrictHostKeyChecking=accept-new "$ALIAS" true 2>"$err"
    rc=$?
    if [ $rc = 0 ]; then
        rm -f "$err"; ok "免密登录可用"; return 0
    fi
    if grep -q 'IDENTIFICATION HAS CHANGED' "$err"; then
        rm -f "$err"
        local kh="$T_HOST"; [ "$T_PORT" = 22 ] || kh="[$T_HOST]:$T_PORT"
        die "$T_HOST 的主机指纹和 known_hosts 里记的不一样（重装过系统？）。确认没问题后执行 ssh-keygen -R '$kh' 再重跑"
    fi
    if ! grep -qiE 'permission denied|too many authentication failures' "$err"; then
        local why; why="$(tail -n 1 "$err")"; rm -f "$err"
        die "连不上 ${T_DEST}：${why:-未知错误}"
    fi
    rm -f "$err"
    if [ "$CHECK" = 1 ]; then
        bad "免密登录不通（部署时会把公钥装上去）"
        return 1
    fi
    authorize_key || die "公钥没装上（密码不对，或 VPS 禁用了密码登录 —— 那就得先用别的方式把 $(tildify "$KEY").pub 放进去）"
    ssh -o BatchMode=yes -o ConnectTimeout=15 "$ALIAS" true 2>/dev/null ||
        die "公钥装上了但还是登不上：检查 VPS 上 sshd 的 PubkeyAuthentication 和 ~ 的权限"
    ok "公钥已装好，免密登录可用"
}

remote_info() {
    local line
    line="$(ssh_ 'printf "%s|%s|%s\n" "$(id -un)" "$HOME" "$(uname -sm)"')" || die "连不上 $ALIAS"
    IFS='|' read -r R_USER R_HOME R_SYS <<EOF
$line
EOF
    info "远端 $R_USER@${ALIAS}，家目录 ${R_HOME}，$R_SYS"
}

# ---- 本机配置 ----

setup_config() {
    head_ "本机配置 → $(tildify "$CFG")"
    local fresh=0 cur rr
    if [ ! -f "$CFG" ]; then
        if [ "$CHECK" = 1 ]; then bad "还没有配置文件（部署时从 config.example 生成）"; return 0; fi
        mkdir -p "$RC_HOME" && cp "$REPO/config/config.example" "$CFG" || die "写 $CFG 失败"
        ok "从 config.example 生成"
        fresh=1
    fi

    cur="$(cfg_get RC_HOST)"
    if [ "$cur" = "$ALIAS" ]; then
        ok "RC_HOST=$ALIAS"
    elif [ "$CHECK" = 1 ]; then
        bad "RC_HOST=${cur:-未设}，不是 $ALIAS"
    else
        [ "$fresh" = 1 ] || [ -z "$cur" ] || warn "RC_HOST 现在是 ${cur}，改掉后 rcsync / sshv / scc / rcweb 都会连 $ALIAS"
        if [ "$fresh" = 1 ] || [ -z "$cur" ] || confirm "RC_HOST 改成 ${ALIAS}？"; then
            set_kv "$CFG" RC_HOST "$ALIAS" && ok "RC_HOST=$ALIAS"
        else
            warn "保留 RC_HOST=${cur}；rcweb 会部署到它那台，所以这次跳过网页端"
            WEB=0
        fi
    fi

    # 不是 root 登录时，远端根目录跟着家目录走（config.example 默认写的是 /root）
    rr="$(cfg_get RC_REMOTE_ROOT)"
    if [ "$R_HOME" != /root ] && [ "${rr:-/root}" = /root ]; then
        if [ "$CHECK" = 1 ]; then
            bad "RC_REMOTE_ROOT=/root，但远端用户 $R_USER 的家目录是 $R_HOME"
        else
            set_kv "$CFG" RC_REMOTE_ROOT "$R_HOME" && set_kv "$CFG" RC_REMOTE_CLAUDE "$R_HOME/.claude" &&
                ok "RC_REMOTE_ROOT=${R_HOME}，RC_REMOTE_CLAUDE=$R_HOME/.claude"
        fi
    else
        ok "RC_REMOTE_ROOT=${rr:-/root}"
    fi

    local p
    p="$(cfg_get RC_PROJECTS)"
    [ "$p" = myapp ] && warn "RC_PROJECTS 还是示例值（myapp mytool my_sdk），改成你要同步的项目"

    [ "$CHECK" = 1 ] && return 0
    head_ "安装命令（install.sh）"
    # --keep-existing：~/.local/bin 里已有的同名命令若不是本仓库的链接（自己改过的版本），
    # 一键部署不该悄悄把它换掉。--no-ignores：拷过去的忽略规则会盖住仓库里的，
    # 以后仓库改了规则也不生效；不拷的话 rcsync 自动用仓库里的
    "$REPO/install.sh" --keep-existing --no-ignores || FAILED+=("install.sh")
}

# ---- VPS 环境 ----
# 这段整体上传到 VPS 用 sh 跑（POSIX sh，不假设有 bash）。参数：install|check [本机 locale]

remote_script() {
    cat <<'REMOTE'
MODE="${1:-install}"
WANT_LOCALE="${2:-}"
FAIL=0

if [ -t 1 ]; then G='\033[32m' Y='\033[33m' R='\033[31m' D='\033[2m' N='\033[0m'; else G= Y= R= D= N=; fi
ok()   { printf "  ${G}✓${N} %s\n" "$*"; }
warn() { printf "  ${Y}!${N} %s\n" "$*"; }
bad()  { printf "  ${R}✗${N} %s\n" "$*"; }
info() { printf "  ${D}%s${N}\n" "$*"; }
have() { command -v "$1" >/dev/null 2>&1; }

# 检测模式下 sudo 不许弹密码
if [ "$(id -u)" = 0 ]; then SUDO=""
elif have sudo; then if [ "$MODE" = check ]; then SUDO="sudo -n"; else SUDO="sudo"; fi
else SUDO=none; fi

OS="$(uname -s)"
[ -r /etc/os-release ] && OS="$(. /etc/os-release; printf '%s' "${PRETTY_NAME:-$ID}")"
PM=""
for p in apt-get dnf yum apk pacman zypper; do have "$p" && { PM="$p"; break; }; done
ok "${OS}（$(uname -m)），包管理 ${PM:-未知}"

APT_UPDATED=0
pkg_install() {
    [ "$SUDO" != none ] || { bad "不是 root 也没有 sudo，装不了：$*"; return 1; }
    [ -n "$PM" ] || { bad "认不出包管理器，手动装：$*"; return 1; }
    info "安装 $*"
    case "$PM" in
        apt-get)
            # 新开的 VPS 常有 unattended-upgrades 占着 dpkg 锁，等它而不是直接失败
            if [ "$APT_UPDATED" = 0 ]; then
                $SUDO apt-get -o DPkg::Lock::Timeout=300 update -qq >/dev/null || return 1
                APT_UPDATED=1
            fi
            $SUDO env DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y -qq "$@" >/dev/null ;;
        dnf)    $SUDO dnf install -y -q "$@" ;;
        yum)    $SUDO yum install -y -q "$@" ;;
        apk)    $SUDO apk add -q --no-cache "$@" ;;
        pacman) $SUDO pacman -Sy --noconfirm --needed "$@" ;;
        zypper) $SUDO zypper -n -q install "$@" ;;
    esac
}

# ---- 基础工具 ----
need=""
for c in bash curl git tmux; do have "$c" || need="$need $c"; done
[ "$MODE" = install ] && [ -n "$need" ] && pkg_install ca-certificates $need
for c in bash curl git tmux; do
    if have "$c"; then ok "$c"; else bad "$c 缺失"; FAIL=1; fi
done

# mosh 在 RHEL 系要先有 EPEL；装不上不算失败，sshv / scc 还能 --ssh
if ! have mosh-server && [ "$MODE" = install ]; then
    case "$PM" in dnf|yum) rpm -q epel-release >/dev/null 2>&1 || pkg_install epel-release ;; esac
    pkg_install mosh
fi
if have mosh-server; then ok "mosh-server"; else warn "mosh-server 缺失 —— sshv / scc 只能加 --ssh"; fi

# ---- mosh 要的 locale ----
# mosh 把本机的 LANG / LC_* 带过去，远端没有同名的 UTF-8 locale 时 mosh-server 直接拒绝启动
norm_locale() { tr 'A-Z' 'a-z' | sed 's/utf-8$/utf8/'; }
has_locale() { locale -a 2>/dev/null | norm_locale | grep -qxF "$(printf '%s\n' "$1" | norm_locale)"; }
gen_locale() {
    case "$PM" in
        apt-get)
            have locale-gen || pkg_install locales || return 1
            if [ -f /etc/locale.gen ]; then
                if grep -q "^# *$1 UTF-8" /etc/locale.gen; then
                    $SUDO sed -i "s/^# *\($1 UTF-8\)/\1/" /etc/locale.gen
                elif ! grep -q "^$1 UTF-8" /etc/locale.gen; then
                    printf '%s UTF-8\n' "$1" | $SUDO tee -a /etc/locale.gen >/dev/null
                fi
            fi
            # Debian 读 /etc/locale.gen、忽略参数；Ubuntu 认参数。两样都给
            $SUDO locale-gen "$1" >/dev/null ;;
        dnf|yum) pkg_install "glibc-langpack-${1%%_*}" ;;
        *) return 1 ;;
    esac
}
if [ -n "$WANT_LOCALE" ] && have locale; then
    if has_locale "$WANT_LOCALE"; then
        ok "locale $WANT_LOCALE"
    else
        [ "$MODE" = install ] && [ "$SUDO" != none ] && gen_locale "$WANT_LOCALE"
        if has_locale "$WANT_LOCALE"; then ok "locale ${WANT_LOCALE}（已生成）"
        else warn "没有 locale $WANT_LOCALE —— mosh 可能报 needs a UTF-8 native locale"; fi
    fi
fi

# ---- Claude Code ----
# 用登录 shell 找：rcweb deploy、scc 里的 tmux 看到的都是这个 PATH
claude_path() { bash -lc 'command -v claude' </dev/null 2>/dev/null; }
fix_path() {
    line='export PATH="$HOME/.local/bin:$PATH"  # remote-claude: claude 装在 ~/.local/bin'
    prof="$HOME/.profile"
    for f in "$HOME/.bash_profile" "$HOME/.bash_login"; do [ -f "$f" ] && { prof="$f"; break; }; done
    files="$prof $HOME/.bashrc"
    [ -f "$HOME/.zshrc" ] && files="$files $HOME/.zshrc"
    for f in $files; do
        grep -qF '# remote-claude: claude' "$f" 2>/dev/null || printf '\n%s\n' "$line" >> "$f"
    done
    info "~/.local/bin 已加进 PATH（${files}）"
}
if [ "$MODE" = install ] && ! claude_path >/dev/null; then
    if [ ! -x "$HOME/.local/bin/claude" ] && have curl && have bash; then
        [ "$PM" = apk ] && pkg_install libgcc libstdc++ ripgrep
        info "安装 Claude Code（官方脚本 https://claude.ai/install.sh）"
        curl -fsSL https://claude.ai/install.sh | bash
    fi
    [ -x "$HOME/.local/bin/claude" ] && ! claude_path >/dev/null && fix_path
fi
if CL="$(claude_path)" && [ -n "$CL" ]; then
    ok "claude $("$CL" --version 2>/dev/null | head -n 1)  $CL"
    if [ -s "$HOME/.claude/.credentials.json" ]; then
        ok "claude 已登录"
    else
        warn "claude 还没登录（没有 ~/.claude/.credentials.json；用 API key 的可忽略）"
    fi
elif [ -x "$HOME/.local/bin/claude" ]; then
    bad "claude 在 ~/.local/bin，但登录 shell 的 PATH 里没有"; FAIL=1
else
    bad "claude 缺失"; FAIL=1
fi

# ---- systemd（rcweb 装成服务要它）----
if have systemctl && [ -d /run/systemd/system ]; then
    ok "systemd"
    if [ "$MODE" = check ]; then
        if [ "$(id -u)" = 0 ]; then st="$(systemctl is-active rcweb 2>/dev/null)"
        else st="$(systemctl --user is-active rcweb 2>/dev/null)"; fi
        if [ "$st" = active ]; then ok "rcweb 服务运行中"; else info "rcweb 服务：${st:-未知}"; fi
    fi
else
    warn "没有 systemd —— 网页端 rcweb 装不成服务"
fi

# ---- 防火墙：mosh 走 UDP 60000-61000 ----
if have mosh-server; then
    if [ "$SUDO" = none ]; then
        info "不是 root 也没有 sudo，看不了防火墙；mosh 要放行 UDP 60000-61000"
    elif have ufw && $SUDO ufw status 2>/dev/null | grep -q '^Status: active'; then
        if $SUDO ufw status | grep -q '60000:61000/udp'; then ok "ufw 已放行 UDP 60000-61000（mosh）"
        elif [ "$MODE" = install ] && $SUDO ufw allow 60000:61000/udp >/dev/null; then ok "ufw 放行 UDP 60000-61000（mosh）"
        else warn "ufw 没放行 UDP 60000-61000，mosh 连不上"; fi
    elif have firewall-cmd && $SUDO firewall-cmd --state >/dev/null 2>&1; then
        if $SUDO firewall-cmd -q --query-port=60000-61000/udp; then ok "firewalld 已放行 UDP 60000-61000（mosh）"
        elif [ "$MODE" = install ] && $SUDO firewall-cmd -q --permanent --add-port=60000-61000/udp &&
             $SUDO firewall-cmd -q --reload; then ok "firewalld 放行 UDP 60000-61000（mosh）"
        else warn "firewalld 没放行 UDP 60000-61000，mosh 连不上"; fi
    else
        info "系统防火墙没开；云厂商的安全组若有限制，要放行 UDP 60000-61000（mosh）"
    fi
fi

exit $FAIL
REMOTE
}

remote_setup() {
    head_ "VPS 环境（${ALIAS}）"
    local mode=install f tt=() loc
    [ "$CHECK" = 1 ] && mode=check
    # 本机实际生效的字符集 locale，规整成 xx_YY.UTF-8；不是这个形状（比如 C.UTF-8）就不管
    loc="${LC_ALL:-${LC_CTYPE:-${LANG:-}}}"
    if [[ "$loc" =~ ^[a-z]{2,3}_[A-Z]{2}\.(UTF-8|utf-8|UTF8|utf8)$ ]]; then loc="${loc%%.*}.UTF-8"; else loc=""; fi

    f="$(remote_script | ssh_ 'f="$(mktemp)" && cat > "$f" && printf %s "$f"')" && [ -n "$f" ] ||
        die "上传检测脚本到 $ALIAS 失败"
    # 安装时给远端一个终端：非 root 用户的 sudo 要在那儿输密码
    [ "$mode" = install ] && [ "$TTY" = 1 ] && tt=(-t)
    ssh ${tt[@]+"${tt[@]}"} -o BatchMode=yes -o ConnectTimeout=15 -o LogLevel=ERROR "$ALIAS" \
        "sh $f $mode $loc; rc=\$?; rm -f $f; exit \$rc" || FAILED+=("VPS 环境")
}

# ---- 网页端 ----

# server/.env 里的监听地址（没写就是 rcweb 的默认值）
web_listen_cur() {
    local l; l="$(envval "$WEB_ENV" RCWEB_LISTEN)"
    printf '%s' "${l:-127.0.0.1:7681}"
}
listen_host() { local h="${1%:*}"; h="${h#\[}"; printf '%s' "${h%\]}"; }
listen_loopback() { case "$(listen_host "$1")" in 127.*|localhost|::1) return 0 ;; *) return 1 ;; esac; }

# 公网模式下浏览器开的地址：绑了具体 IP 就用它，否则用部署目标的主机（和 rcweb 的 public_url 同一规则）
web_url() {
    local l h
    l="$(web_listen_cur)"; h="$(listen_host "$l")"
    case "$h" in ""|0.0.0.0|::|127.*|localhost|::1) h="$T_HOST" ;; esac
    case "$h" in *:*) h="[$h]" ;; esac
    printf 'http://%s:%s' "$h" "${l##*:}"
}
web_up() { curl -fsS -m 5 -o /dev/null "$(web_url)/healthz" 2>/dev/null; }

# 按 PUBLIC 改 RCWEB_LISTEN，端口不变；已经是想要的那一类（比如绑了具体 IP）就不动
web_listen() {
    local cur want
    cur="$(web_listen_cur)"
    if [ "$PUBLIC" = 1 ]; then
        listen_loopback "$cur" || { ok "RCWEB_LISTEN=${cur}（公网可访问）"; return 0; }
        want="0.0.0.0:${cur##*:}"
    else
        listen_loopback "$cur" && { ok "RCWEB_LISTEN=${cur}（只走隧道 / Tailscale）"; return 0; }
        want="127.0.0.1:${cur##*:}"
    fi
    set_kv "$WEB_ENV" RCWEB_LISTEN "$want" && ok "RCWEB_LISTEN=${want}（原来是 ${cur}）"
}

web_password() {
    if [ ! -f "$WEB_ENV" ]; then
        cp "$REPO/server/.env.example" "$WEB_ENV" || return 1
        chmod 600 "$WEB_ENV"
        info "从 .env.example 生成 server/.env"
    fi
    # 开在公网上，能登录就能以 root 跑命令，密码要求高一点
    local pw pw2 gen=0 min=8
    [ "$PUBLIC" = 1 ] && min=12
    pw="$(envval "$WEB_ENV" RCWEB_PASSWORD)"
    if [ -n "$pw" ] && [ "$pw" != change-me ]; then
        ok "登录密码已在 server/.env 里"
        [ ${#pw} -ge $min ] ||
            warn "密码只有 ${#pw} 位，开在公网上至少要 ${min} 位：删掉 server/.env 里的 RCWEB_PASSWORD 再跑一次会自动生成"
        return 0
    fi
    pw=""
    if interactive; then
        while :; do
            printf '  rcweb 登录密码（直接回车自动生成）: '
            IFS= read -rs pw; echo
            [ -n "$pw" ] || break
            # 最后要进 systemd 的 EnvironmentFile，空白、引号、反斜杠在那里都有特殊含义
            case "$pw" in *[[:space:]\"\'\\]*) warn "密码里别用空白、引号和反斜杠"; continue ;; esac
            [ ${#pw} -ge $min ] || { warn "至少 ${min} 位"; continue; }
            printf '  再输一次: '
            IFS= read -rs pw2; echo
            [ "$pw" = "$pw2" ] && break
            warn "两次不一致"
        done
    fi
    if [ -z "$pw" ]; then
        pw="$(LC_ALL=C tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 20)"
        gen=1
    fi
    set_kv "$WEB_ENV" RCWEB_PASSWORD "$pw" && chmod 600 "$WEB_ENV" || return 1
    if [ "$gen" = 1 ]; then ok "已生成登录密码：${pw}（存在 server/.env）"; else ok "登录密码已写入 server/.env"; fi
}

setup_web() {
    head_ "网页端 rcweb"
    [ "$WEB" = 1 ] || { info "跳过"; return 0; }
    if [ "$CHECK" = 1 ]; then
        info "服务状态见上面「VPS 环境」"
        [ "$PUBLIC" = 1 ] || return 0
        if listen_loopback "$(web_listen_cur)"; then
            bad "server/.env 的 RCWEB_LISTEN 还是回环地址（部署时改成 0.0.0.0）"; FAILED+=("网页端公网访问")
        elif web_up; then
            ok "公网可访问 $(web_url)"
        else
            bad "本机连不上 $(web_url)（云厂商安全组要放行 TCP $(web_listen_cur | sed 's/.*://')）"; FAILED+=("网页端公网访问")
        fi
        return 0
    fi
    if ! ssh_ '[ -d /run/systemd/system ]'; then
        warn "$ALIAS 没有 systemd，跳过"; FAILED+=("rcweb"); return 0
    fi
    if ! local_ok go || ! local_ok node; then
        bad "本机 go / node 不可用，构建不了"; FAILED+=("rcweb"); return 0
    fi
    web_password && web_listen || { bad "写 server/.env 失败"; FAILED+=("rcweb"); return 0; }
    "$REPO/bin/rcweb" deploy || { FAILED+=("rcweb"); return 0; }
    # rcweb deploy 已经把连不上的原因说过了，这里只记进最后的汇总
    [ "$PUBLIC" = 1 ] || return 0
    if web_up; then WEB_URL="$(web_url)"; else FAILED+=("网页端公网访问"); fi
}

# ---- 主流程 ----

resolve_info
local_env
if setup_ssh; then
    remote_info
    setup_config
    remote_setup
    setup_web
else
    FAILED+=("SSH")
fi

if [ ${#FAILED[@]} -gt 0 ]; then
    head_ "$([ "$CHECK" = 1 ] && echo '检测完成' || echo '部署完成')，有没过的：${FAILED[*]}"
    [ "$CHECK" = 1 ] && info "去掉 --check 再跑一次会自动补齐能装的部分"
    exit 1
fi
head_ "$([ "$CHECK" = 1 ] && echo '检测通过' || echo '部署完成')"
[ "$CHECK" = 1 ] && exit 0
[ -n "$WEB_URL" ] && printf '  网页端：浏览器开 %s ，密码是 server/.env 里的 RCWEB_PASSWORD\n\n' "$WEB_URL"
cat <<TIP
  下一步：
    1. 远端 claude 没登录的话：ssh ${ALIAS}，运行一次 claude 按提示登录
    2. 在 $(tildify "$CFG") 的 RC_PROJECTS 里写要同步的项目，然后 rcsync up
    3. 进远端跑：cd ~/workspace/<项目> && scc
    4. 全面体检：rcsync doctor；以后再跑 ./deploy.sh --check 只检测不改
TIP
