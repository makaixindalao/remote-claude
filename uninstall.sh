#!/usr/bin/env bash
# uninstall.sh — 卸载 remote-claude 的命令软链接
#
# 只摘链接。配置（~/.config/remote-claude）和 Mutagen 同步会话都保留，
# 因为它们代表的是"你两端的数据关系"，不该被一次卸载顺手抹掉。
# 真要停同步：先 rcsync down，再跑这个。

set -uo pipefail

REPO="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"

for f in "$REPO"/bin/*; do
    n="$(basename "$f")"
    t="$BIN_DIR/$n"
    if [ -L "$t" ] && [ "$(readlink "$t")" = "$f" ]; then
        rm -f "$t" && printf '  已移除 %s\n' "$t"
        # 有备份就放回去
        b="$(ls -t "$t".bak.* 2>/dev/null | head -1)"
        [ -n "$b" ] && mv "$b" "$t" && printf '  已还原备份 %s\n' "$b"
    fi
done
printf '\n配置仍在 %s\n' "${XDG_CONFIG_HOME:-$HOME/.config}/remote-claude"
printf 'Mutagen 会话仍在运行，要停：rcsync down（或 mutagen sync terminate <名字>）\n'
