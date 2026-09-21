# scc / ccv

## scc —— 在远端项目目录里跑 Claude Code

```sh
cd ~/workspace/myapp
scc                      # 在 vps 的 /root/myapp 里起 claude
scc -r                   # 任意 claude 参数原样透传
scc -p "写个函数"         # 带引号的参数也能正确传递
scc --session <name>     # 自定义 tmux 会话名（默认 cc-<目录名>）
scc --new                # 同目录已有 claude 时另开一个（cc-xxx-2、-3…）
scc --ssh                # 走 SSH 而非 mosh
scc --scc-help           # 帮助（-h 要留给 claude）
```

它做的事：把当前目录映射成远端目录 → 在名为 `cc-<目录名>` 的 tmux 会话里
起 `claude` → 用 mosh 连过去。会话名固定，所以**同一个项目重复敲 `scc`
是接回正在跑的那个，而不是开出第二个 claude**。

除了自己那几个开关，其余参数一律透传给 claude。

### 三个坑和对应的处理

**默认补 `--dangerously-skip-permissions`。** 你已经显式传过就不重复补。

**`IS_SANDBOX=1`。** claude 拒绝以 root 使用 `--dangerously-skip-permissions`
（"cannot be used with root/sudo privileges for security reasons"），
而 VPS 的 SSH 用户就是 root。这是 Claude Code 给容器/沙箱环境留的绕过口。

**`env -u TMUX -u TMUX_PANE`。** claude 一旦检测到 `TMUX` 变量就把颜色降级到
256 色调色板，它本要输出的暗灰（#888888/#999999）被压成 244/246 号色，明显偏白。
实测对照：剥掉该变量 → 24-bit 15 处 / 256 色 6 处；保留 → 24-bit 0 处 / 256 色 22 处。
`FORCE_COLOR=3` 覆盖不了它，只能把变量摘掉。代价是 claude 内部跑的命令感知不到
自己在 tmux 里（tmux 的会话持久性完全不受影响）。

### 参数传递

远端拿到的是一整条 shell 字符串，所以每个参数都做了 POSIX 单引号转义。
没用 `printf %q`：bash 3.2 会把非 ASCII 转成 `$'\xxx'` 这种 bash 专有写法，
远端 `/bin/sh` 是 dash 时不认，中文参数会被打散。

## ccv —— 本地跑 Claude，但用远端那套账号

```sh
ccv --login        # 在独立配置目录里走一次 /login（推荐）
ccv --import-vps   # 把 vps 上的凭证搬过来（快，但两端共用 refreshToken 会互相踢掉线）
ccv [参数...]      # 之后直接用
```

原理是 `CLAUDE_CONFIG_DIR` 把配置、凭证、会话历史整体隔离到 `~/.claude-vps`，
和默认账号（`~/.claude`）互不干扰，可以同时开着。

**什么时候用 ccv 而不是 scc**：UI 全本地渲染 —— 打字零延迟、dim 灰色正常、
剪贴板直通、滚动跟手，远程终端那一整串问题全部消失。文件也是本地直接读写，
由 Mutagen 同步过去。代价是算力和网络出口都在本机。

反过来说，`scc` 的不可替代之处是：**活干在 VPS 上**。合上电脑它继续跑，
出口 IP 是 VPS 的，长任务不占本机。
