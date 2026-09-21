# skills 与会话同步

代码同步解决了"两端看到同样的文件"，但 Claude 自己的状态还留在各自机器上：
一端写的 skill 另一端用不了，一端跑到一半的会话另一端接不上。这两件事分别对应
`~/.claude/skills` 和 `~/.claude/projects`。

## skills

整个目录一对一同步，没有路径推导问题：

```
~/.claude/skills   ⇄   /root/.claude/skills
```

```sh
rcsync up skills
```

默认忽略 `synced/` —— 那是 claude.ai 下发的技能（bucket 目录加 `.bucket-*` 标记文件），
Claude Code 自己有下发和清理逻辑，再叠一层双向同步只会互相打架，让两端各自去拉就行。
手写的 skill 不在那个目录下，不受影响。

规则在 `~/.config/remote-claude/ignores/claude-skills.ignore`。

## 会话记录

会话正文在 `~/.claude/projects/<编码过的项目路径>/<sessionId>.jsonl`。
麻烦在于那个目录名是**项目绝对路径编码出来的**，而两端的绝对路径不一样：

```
本地 /Users/you/workspace/myapp
  →  ~/.claude/projects/-Users-you-workspace-myapp

远端 /root/myapp
  →  /root/.claude/projects/-root-myapp
```

编码规则是「非字母数字一律换成 `-`」。实测对照：
`/root/my_sdk` → `-root-my-sdk`（下划线也变 `-`）；
`/root/mytool/.claude/worktrees/…` → `-root-mytool--claude-worktrees-…`（`/.` 变 `--`）。

所以**不能整个 `projects/` 目录对同步** —— 那样两端会各自多出一份对方命名风格的
目录，数据翻倍且毫无意义。只能按项目一对一配，`rcsync` 会从
`RC_LOCAL_ROOT/RC_REMOTE_ROOT` 自动算出两边的目录名：

```sh
rcsync up sessions            # 全部项目的会话
rcsync up sess-myapp      # 只一个
rcsync list                   # 看推导出来的目录名对不对
```

配置里 `RC_SESSION_PROJECTS` 默认跟随 `RC_PROJECTS`，只想同步部分项目就单独列。

会话目录的同步额外带了 `--default-file-mode=0600 --default-directory-mode=0700`：
transcript 里可能有你粘进去的密钥、日志片段，不该在另一端变成 0644。

### 能做到什么，做不到什么

**能**：本机 `claude --resume` 能看到并接上远端跑过的会话，反之亦然。
正文、工具调用记录、时间线都是完整的。

**做不到**（或者说要知道的代价）：

- 记录里的 `cwd` 和一切绝对路径是**写它的那一端**的。跨端 resume 时正文照读，
  但依赖历史快照的功能（如 `/rewind` 要用 `~/.claude/file-history/`）跨不过去 ——
  那部分数据默认没同步，要的话用 `RC_EXTRA_PAIRS` 自己加一对。
- **同一个会话不要两端同时开。** 两端各自往同一个 `.jsonl` 追加，
  Mutagen 会判定冲突并把两份都留着，你得手工挑一边。不同会话（不同 sessionId）
  是不同文件，不存在这个问题。
- Claude Code 自己会清理过期会话。一端清掉了，删除会同步到另一端。
  想留档就别只靠这个目录。

## 还想同步别的

`~/.claude` 下的 `agents/`、`commands/` 之类，用 `RC_EXTRA_PAIRS` 加：

```sh
RC_EXTRA_PAIRS=(
    "agents|$HOME/.claude/agents|/root/.claude/agents"
    "commands|$HOME/.claude/commands|/root/.claude/commands"
)
```

**不建议同步 `settings.json` 和 `.credentials.json`**：
前者两端的路径、hooks、环境往往不一样；后者两端共用同一份 refreshToken 会
互相把对方刷掉线（`ccv --import-vps` 的告警说的就是这件事）。
