# 排查

先跑 `rcsync doctor` —— 依赖、Mutagen 守护进程、SSH 连通性、远端工具、
每个目标两端目录是否存在，一次全查。

## 同步

**`rcsync status` 显示 `not-synced`**
没建过同步，`rcsync up` 即可。若 `doctor` 里这个目标两端目录都是"无"，
那是配置里的项目名写错了。

**某个目标一直 `Connecting to beta`**
SSH 连不上或远端 mutagen agent 版本不匹配。先 `ssh <host> true` 确认能连，
再看 `mutagen sync list <名字>` 的报错。agent 是 Mutagen 自己推送到
远端 `~/.mutagen/agents/` 的，本机升级 mutagen 后它会自动重推。

**改了文件但另一端没变**
`rcsync status` 看那个目标的状态。`Watching` 说明它在等变更，
`rcsync flush` 强推一轮。还是不动就看是不是被忽略规则挡了 ——
`mutagen sync list --long <名字>` 会列出生效的全部 ignore。

**冲突数一直不为 0**
`rcsync conflicts` 看是哪些文件，`rcsync resolve <目标> alpha|beta` 挑一端。
`.git/index` 反复冲突通常是两端同时在跑 git 命令。

**暂停一个会话后 `status` 全空**
已修：被 pause 的会话内嵌状态是 nil，旧模板取 `.Status` 会让整张表查询报错。
若自己改过 `mu_refresh` 的模板，记得保留 `{{if .SessionState}}` 那层判断。

**构建时同步狂转**
`rcsync pause <目标>` 再构建，或者把产物目录加进
`~/.config/remote-claude/ignores/<目标名>.ignore`。

## 连接

**mosh 连不上，SSH 却没问题**
UDP 60000-61000 没放行。临时绕开：`sshv --ssh` / `scc --ssh`。

**mosh 里 claude 的灰色提示文字偏亮**
mosh 丢弃 SGR 2（dim）属性，这是协议层面的，改不了。要原样显示就 `--ssh`，
代价是失去本地回显预测，打字要等一个往返。

**接回会话后窗口变得很窄**
残留的僵尸 client 把尺寸拉成了最小值。`sshv` 用 `tmux new -A -D` 正是为了这个，
手工 `tmux attach` 时记得加 `-D`。

**`sshv <name>` 说会话不存在，但 `sshv ls` 明明有**
名字里有 `.` 或 `:`，tmux 不接受。`scc` 生成会话名时已经把这两个字符换成 `-`。

**同一个项目敲两次 `scc`，第二次把第一个窗口踢下线了**
不该发生 —— `sshv` 会检测本机是否已有窗口连着同名会话并自动另开。
若真发生了，看 `~/.config/sshv/active` 里有没有残留的死 PID。

## Claude

**远端起不来，报 root 不能用 `--dangerously-skip-permissions`**
`scc` 会自动补 `IS_SANDBOX=1`。手工在远端敲 claude 时要自己带上。

**跨端 `--resume` 看不到对面的会话**
`rcsync status sessions` 确认那个项目的会话同步在跑，
`rcsync list` 确认推导出的两端目录名和实际一致
（`ls ~/.claude/projects` 对一眼）。

**本地和远端同时开了同一个会话，jsonl 冲突了**
挑一端为准：`rcsync resolve sess-<项目> alpha|beta`。
之后别再两端同时开同一个 sessionId。
