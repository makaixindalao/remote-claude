#!/usr/bin/env bash
# install.sh — 把 remote-claude 装到本机
#
#   ./install.sh              安装（命令软链接 + 配置 + 环境检查）
#   ./install.sh --force      覆盖已存在的配置文件
#   ./install.sh --bin-dir D  命令装到 D（默认 ~/.local/bin）
#   ./install.sh --keep-existing  已有的同名命令不是本仓库的链接时保留不动（deploy.sh 用）
#   ./install.sh --no-ignores     不往配置目录拷忽略规则：rcsync 找不到时用仓库里的，随仓库更新
#
# 装的是软链接而不是拷贝：改仓库里的脚本立刻生效，不用重装。
# 原有的同名文件会备份成 <名字>.bak.<时间戳>，不会被直接覆盖。

set -uo pipefail

REPO="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"
RC_HOME="${RC_HOME:-${XDG_CONFIG_HOME:-$HOME/.config}/remote-claude}"
FORCE=0
KEEP=0
IGNORES=1

while [ $# -gt 0 ]; do
    case "$1" in
        --force)   FORCE=1; shift ;;
        --keep-existing) KEEP=1; shift ;;
        --no-ignores) IGNORES=0; shift ;;
        --bin-dir) BIN_DIR="$2"; shift 2 ;;
        -h|--help) sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) printf 'install: 未知参数 %s\n' "$1" >&2; exit 1 ;;
    esac
done

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; }
head_() { printf '\n\033[1m%s\033[0m\n' "$*"; }

TS="$(date +%Y%m%d%H%M%S)"

# ---- 依赖 ----
head_ "依赖检查"
MISSING=""
for c in mutagen mosh tmux ssh; do
    if command -v "$c" >/dev/null 2>&1; then
        ok "$c"
    else
        bad "$c 缺失"
        MISSING="$MISSING $c"
    fi
done
if [ -n "$MISSING" ]; then
    warn "装一下：brew install${MISSING/ mutagen/ mutagen-io\/mutagen\/mutagen}"
    warn "（mutagen 缺失时 rcsync 不能用；mosh 缺失时 sshv/scc 需要加 --ssh）"
fi

# ---- 命令 ----
head_ "安装命令 → $BIN_DIR"
mkdir -p "$BIN_DIR" || exit 1
for f in "$REPO"/bin/*; do
    n="$(basename "$f")"
    t="$BIN_DIR/$n"
    if [ -L "$t" ]; then
        cur="$(readlink "$t")"
        if [ "$cur" = "$f" ]; then ok "${n}（已是本仓库的链接）"; continue; fi
        if [ "$KEEP" = 1 ]; then warn "${n} 保留不动（现在指向 ${cur}）"; continue; fi
        rm -f "$t"
    elif [ -e "$t" ]; then
        if [ "$KEEP" = 1 ]; then warn "${n} 保留不动（是你自己的文件，不是本仓库的链接）"; continue; fi
        mv "$t" "$t.bak.$TS" || { bad "$n 备份失败"; continue; }
        warn "$n 原文件已备份为 $n.bak.$TS"
    fi
    ln -s "$f" "$t" && ok "$n"
done

case ":$PATH:" in
    *":$BIN_DIR:"*) ok "PATH 已包含 $BIN_DIR" ;;
    *) warn "PATH 不含 ${BIN_DIR}，加到 ~/.zshrc："
       printf '      export PATH="%s:$PATH"\n' "$BIN_DIR" ;;
esac

# ---- 配置 ----
head_ "配置 → $RC_HOME"
mkdir -p "$RC_HOME/ignores" || exit 1
if [ -f "$RC_HOME/config" ] && [ "$FORCE" != 1 ]; then
    ok "config 已存在，保留不动（--force 覆盖）"
else
    [ -f "$RC_HOME/config" ] && cp "$RC_HOME/config" "$RC_HOME/config.bak.$TS"
    cp "$REPO/config/config.example" "$RC_HOME/config" && ok "config 已写入"
    warn "按你的主机和项目改一下：$RC_HOME/config"
fi
for f in "$REPO"/config/ignores/*.ignore; do
    n="$(basename "$f")"
    if [ "$IGNORES" = 0 ]; then
        [ -f "$RC_HOME/ignores/$n" ] && ok "ignores/${n}（用你改过的这份）" || ok "ignores/${n}（用仓库里的）"
    elif [ -f "$RC_HOME/ignores/$n" ] && [ "$FORCE" != 1 ]; then
        ok "ignores/$n 已存在，保留不动"
    else
        cp "$f" "$RC_HOME/ignores/$n" && ok "ignores/$n"
    fi
done

# ---- SSH ----
head_ "SSH"
RC_HOST="$(sed -n 's/^RC_HOST=//p' "$RC_HOME/config" | tr -d '"' | head -1)"
RC_HOST="${RC_HOST:-vps-64}"
mkdir -p "$HOME/.ssh/cm" && chmod 700 "$HOME/.ssh/cm" && ok "ControlPath 目录 ~/.ssh/cm"

if ssh -G "$RC_HOST" 2>/dev/null | grep -qi "^hostname $RC_HOST$"; then
    # ssh -G 把未定义的 Host 原样回显成 hostname，说明 ~/.ssh/config 里没有这个块
    bad "~/.ssh/config 里没有 Host $RC_HOST"
    warn "把 $REPO/config/ssh.config.snippet 的内容追加进去，填上 HostName/Port"
else
    ok "Host $RC_HOST 已配置"
    for k in serveraliveinterval controlmaster compression; do
        v="$(ssh -G "$RC_HOST" 2>/dev/null | sed -n "s/^$k //p" | head -1)"
        case "$k:$v" in
            serveraliveinterval:0|serveraliveinterval:) warn "没设 ServerAliveInterval，长时间空闲会被 NAT 掐断" ;;
            controlmaster:no|controlmaster:) warn "没开 ControlMaster，sshv 每次查会话都要重新握手" ;;
            compression:no) warn "没开 Compression，高延迟链路上会更慢" ;;
            *) ok "$k = $v" ;;
        esac
    done
fi

head_ "完成"
cat <<TIP
  下一步：
    1. 改配置：   $RC_HOME/config
    2. 体检：     rcsync doctor
    3. 起同步：   rcsync up
    4. 进远端跑： cd ~/workspace/<项目> && scc

  命令一览：
    rcsync   同步（代码 / skills / Claude 会话）
    sshv     mosh+tmux 会话管理
    scc      在远端项目目录里跑 Claude Code
    ccv      在本地跑 Claude Code，但用远端那套账号
TIP
