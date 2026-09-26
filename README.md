# remote-claude

把「Claude Code 跑在 VPS 上、人坐在 Mac 前」这件事固化成一套可安装的配置。

VPS 上跑 Claude 有三个绕不开的问题，这个项目就是这三个问题的答案：

| 问题 | 方案 | 命令 |
|---|---|---|
| 代码怎么在两端保持一致 | Mutagen 双向同步（含 `.git`） | `rcsync` |
| 网一抖就断、断了活就没了 | SSH 保活 + mosh + tmux | `sshv` |
| 每次进远端都要敲一串 | 目录自动映射 + 会话自动接回 | `scc` |

外加两件让"换台机器继续干"真正成立的事：**skills 同步**和 **Claude 会话记录同步**。

## 快速开始

```sh
cd ~/workspace/tool/remote-claude  # 或你放它的任何位置
./install.sh                      # 软链命令到 ~/.local/bin，配置写到 ~/.config/remote-claude
vi ~/.config/remote-claude/config # 填主机别名和要同步的项目
rcsync doctor                     # 体检：依赖、SSH、远端工具、路径推导
rcsync up                         # 起同步
cd ~/workspace/myapp && scc   # 在远端对应目录里跑 claude
```

装之前先确认 `~/.ssh/config` 里有目标主机的 Host 块（没有就抄 `config/ssh.config.snippet`）。
**所有东西都认 Host 别名而不是 IP** —— 保活、复用、端口、密钥都写在那个块里，
Mutagen 和 mosh 都要用。

## 四个命令

```
rcsync up|down|status|flush|conflicts|resolve|doctor   同步（代码 / skills / 会话）
sshv   [add|ls|rm|-c] [名字]                           mosh+tmux 会话管理
scc    [claude 的任意参数]                              在远端当前项目目录里跑 claude
ccv    [claude 的任意参数]                              在本地跑 claude，但用远端那套账号
rcweb  deploy|status|logs|tunnel|dev                   浏览器里用 VPS 上的 Claude（见 docs/06-web.md）
```

每个都有 `--help`（scc 是 `--scc-help`，因为 `-h` 要留给 claude）。

## 它是怎么串起来的

```
       Mac                                   VPS
  ~/workspace/X   ←── Mutagen 双向同步 ──→   /root/X
  ~/.claude/skills ←────────────────────→   /root/.claude/skills
  ~/.claude/projects/-Users-you-workspace-X
                   ←────────────────────→   /root/.claude/projects/-root-X

  scc  ──→ sshv ──→ mosh(UDP) ──→ tmux 会话 cc-X ──→ claude（跑在 /root/X）
```

一条主线贯穿始终：**本地 `$RC_LOCAL_ROOT/X` 恒等于远端 `$RC_REMOTE_ROOT/X`**。
`sshv` 靠它把当前目录翻译成远端目录，`rcsync` 靠它决定同步哪一对，
Claude 的会话目录名也是从这两条路径各自推导出来的。所以配置里那两行路径
是整套东西的地基，改它等于改全部。

## 目录

```
bin/
  rcsync      Mutagen 同步统一入口（本项目新增）
  sshv        mosh + tmux 会话管理
  scc         远端跑 Claude Code
  ccv         本地跑 Claude Code、用远端账号
  rcweb       网页端的部署与管理
server/       网页端：Go 服务（VPS 上跑）+ ui/（React + shadcn/ui，编进二进制）
config/
  config.example       主配置（主机、路径映射、要同步的项目）
  ssh.config.snippet   推荐的 ~/.ssh/config Host 块
  ignores/             同步忽略规则，按目标名叠加
docs/
  01-code-sync.md      代码同步：选型、配置、冲突
  02-mosh-tmux.md      防断连：保活、mosh、tmux、sshv 的设计
  03-scc.md            scc / ccv
  04-claude-state.md   skills 与会话同步
  05-troubleshooting.md
  06-web.md            网页端 rcweb：部署、访问、安全、用法
```

## 已经手工配过的怎么办

`rcsync` 认的是**两端路径这一对**，不是会话名。你之前手工 `mutagen sync create`
出来的会话，只要路径对得上就会被直接认领（`status` 里会显示它原来的名字），
不会重复建一条指向同一对目录的同步。所以 `rcsync up` 对既有环境是安全的。
