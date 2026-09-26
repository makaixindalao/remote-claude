# skills 与会话同步

代码同步解决了"两端看到同样的文件"，但 Claude 自己的状态还留在各自机器上：
一端写的 skill 另一端用不了，一端跑到一半的会话另一端接不上。这两件事分别对应
`~/.claude/skills` 和 `~/.claude/projects`。

## skills

整个目录一对一同步，没有路径推导问题：

```
~/.claude/skills   ⇄   /root/.claude/skills      （目标 skills）
~/.agents/skills   ⇄   /root/.agents/skills      （目标 agent-skills，本机有这个目录才会配）
```

```sh
rcsync up skills          # 两对一起
```

为什么要带上 `~/.agents/skills`：`npx skills` 这类工具把技能本体装在那里，
`~/.claude/skills` 里只放 `../../.agents/skills/<名>` 这样的相对软链。
Mutagen 默认的 portable 软链模式会**跳过越出同步根的软链**，只同步
`~/.claude/skills` 的话，这些技能一个都过不去。所以 skills 这一对用
`--symlink-mode=posix-raw` 把链接原样同步，链接指向的目录再单独配一对；
两端的相对布局一致，链接在两端都能解析。

默认忽略 `synced/` —— 那是 claude.ai 下发的技能（bucket 目录加 `.bucket-*` 标记文件），
Claude Code 自己有下发和清理逻辑，再叠一层双向同步只会互相打架，让两端各自去拉就行。
手写的 skill 不在那个目录下，不受影响。

规则在 `claude-skills.ignore` / `agent-skills.ignore`。rcsync 逐个文件找：
`~/.config/remote-claude/ignores/` 里有同名文件就用它，没有就用仓库 `config/ignores/` 里的。

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

项目的自动记忆 `memory/`（`MEMORY.md` 索引加一条条记忆文件）也在这个目录下，
所以会话同步顺带就把记忆同步了，两端的 Claude 读写的是同一份记忆。

`claude-sessions.ignore` 只排除了 Remote Control（`claude rc`）的项目级指针
`bridge-pointer.json`（记着本机 rc 进程 pid 和它挂的 claude.ai 会话）。
会话子目录里的 `ccr-tip.json`（桥接事件流游标）**故意不排除**：rc 会话的子目录里
常常只有它一个文件，排除后另一端收到的是空目录，Claude Code 会清理空的会话子目录，
这个删除传回来时 Mutagen 删不掉一个还装着被忽略文件的目录，就会报冲突。
**原则：不要忽略一个可能独占某个目录的文件。**

### worktree 里的会话

Claude 自建的 worktree（`claude -w`、Remote Control 派生）在 `<项目>/.claude/worktrees/<名>`，
代码在项目目录里，已经随代码同步走了；会话则落在单独的项目目录
`<项目目录名>--claude-worktrees-<名>`。worktree 随时新增、名字不可预知，所以 rcsync
不把它们写进配置，而是每次按这个前缀从两端现有的目录里找，一个 worktree 配一对
（目标名 `sess-<项目>-wt-<名>`，归在 `sessions` 分组里，也可以单独用 `worktree` 分组）：

```sh
rcsync list worktree      # 看现在发现了哪些
rcsync autodiscover       # 给新出现的补建同步（只新建、不确认、无事不出声）
```

`autodiscover` 由 launchd 每 5 分钟跑一次，模板在
`config/launchd/com.remote-claude.rcsync-autodiscover.plist`（安装方法写在文件头里），
日志 `~/Library/Logs/rcsync-autodiscover.log` 只在真的新建了同步时才有内容。
所以新 worktree 里的会话最多 5 分钟后开始同步。worktree 删掉后它的会话目录还在，
对应的同步会一直留着（一条小同步在远端约占 11–16MB 内存、CPU 几乎为 0）；
确实不要了就 `rcsync down <目标>`，再把两端的目录删掉。

**worktree 的 git 在另一端能不能用**取决于路径。git 的 worktree 链接是绝对路径
（worktree 里的 `.git` 文件写着 `gitdir: <主仓库>/.git/worktrees/<名>`，主仓库里
`.git/worktrees/<名>/gitdir` 反过来指向 worktree），而 `.git` 是整目录同步的，
所以一端建的 worktree 到了另一端，路径全是对面的。做法是在 VPS 上建一个软链，
让 Mac 的路径在 VPS 上也能解析：

```sh
ssh vps 'mkdir -p /Users/you && ln -s /root /Users/you/workspace'
```

这样 **Mac 上建的** worktree 在 VPS 上 git 照常可用（实测：建软链前
`fatal: not a git repository`、`worktree list` 标 prunable；建后都正常）。
反方向做不到：Mac 上建不了 `/root`，**VPS 上建的** worktree（比如 rc 派生的
`bridge-cse_*`）代码会同步到 Mac，但在 Mac 上用不了 git。
彻底两向可用要用 git ≥ 2.48 的相对路径 worktree（`worktree.useRelativePaths`），
代价是仓库会带上 `extensions.relativeWorktrees`，所有访问这个仓库的 git 都得 ≥ 2.48。

另外，从 worktree 起的会话在交互式选择器里是和主仓库的会话合在一起列的
（按仓库汇总，不是按目录），默认选中的是列表第一条，不一定是你要的那个 —— 确认好再回车。

### 能做到什么，做不到什么

**能**：本机 `claude --resume` 能看到并接上远端跑过的会话，反之亦然。
正文、工具调用记录、时间线都是完整的。

**不需要改写路径。** 记录里的 `cwd` 和一切绝对路径是写它的那一端的，但
Claude Code 只按会话文件所在的项目目录归属会话，不拿记录里的 `cwd` 去比对：
2026-09-23 在 2.1.280 上两个方向都实测过（`cwd=/root/…` 的会话在 Mac 上、
`cwd=/Users/…` 的会话在 VPS 上），`--resume <id>` 和交互式选择器都能直接接上，
不会出现 "This conversation is from a different directory"。
所以**不要**在拷贝会话时把 `/root/x` 改写成 `/Users/you/workspace/x` —— 改写会让
两端同一个文件内容不同，一开同步就全是冲突。

**做不到**（或者说要知道的代价）：

- 依赖历史快照的功能（如 `/rewind` 要用 `~/.claude/file-history/`）跨不过去 ——
  那部分数据默认没同步，要的话用 `RC_EXTRA_PAIRS` 自己加一对。
- 两端登录的若不是同一个账号：用 `claude rc` 起的会话拿到另一端 resume，
  对话本身照常接上，但 Remote Control 的桥接会被否决重开
  （记录里会出现 `history-suppression` / `restored_owner_mismatch`），
  claude.ai 网页那一侧看不到之前的历史。
- 从项目**子目录**里起的 claude（比如在 `myapp/docs` 里）会落到另一个项目目录
  `-…-myapp-docs`，不在 `sess-myapp` 的同步范围内（worktree 例外，见上）。
  不按 `<项目目录名>-` 前缀一并收进来，是因为它和同级的 `myapp-docs/` 这类兄弟目录
  编码后长得一样，分不出来。
- 以 `-p` 非交互方式起的会话不进交互式选择器，要用 `claude --resume <id>` 接。
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
