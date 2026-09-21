# 代码同步

## 为什么是 Mutagen

远端开发的文件问题只有三种解法，其余两种都试过会更慢：

- **sshfs / VS Code Remote 挂载**：每次 `ls`、每次读文件都是一次往返。
  231ms 的链路上，`git status` 和编辑器的文件索引会把你钉死。
- **git push/pull 当同步用**：每改一行就要提交一次，或者堆一串 `wip` 提交。
  Claude 在远端一轮能改十几个文件，用 git 传就是在给自己制造噪音历史。
- **Mutagen**：两端各存一份完整的本地文件，后台增量双向同步。
  读写都是本地速度，网络只承担 diff。这是唯一一个"编辑器感觉不到远端存在"的方案。

代价是它不再是"一份文件"，而是"两份文件 + 一个收敛过程"，所以有了冲突这回事（见下）。

## 路径约定

```
本地 $RC_LOCAL_ROOT/X   ⇄   远端 $RC_REMOTE_ROOT/X
默认  ~/workspace/X          /root/X
```

两端同名不是审美，是 `sshv` 能把"我现在在哪"翻译成"远端去哪"的前提，
也是会话目录名推导的输入。两端不同名的项目在配置里写 `本地名:远端名`。

## 配置

`~/.config/remote-claude/config`：

```sh
RC_HOST=vps-64                    # ~/.ssh/config 里的别名
RC_LOCAL_ROOT="$HOME/workspace"
RC_REMOTE_ROOT=/root
RC_PROJECTS=(
    myapp
    mytool
    my_sdk
)
```

## 忽略规则

`~/.config/remote-claude/ignores/` 下每个文件一行一条，`#` 是注释。
`rcsync` 建同步时把它们逐行转成 `--ignore=`：

- `common.ignore` —— 所有代码目录都叠加
- `<目标名>.ignore` —— 只对那个目标生效（目标名见 `rcsync list`）

该忽略什么：依赖目录、构建产物、二进制、体积大又能重建的东西。
不该忽略什么：`.git` 本身。两端共享同一份提交历史，是这套方案比 sshfs
好用的核心原因之一 —— 在哪一端 commit 都行，另一端立刻看得到。

代价是 `.git/index` 偶尔会冲突（两端同时跑了 git 命令）。锁文件和临时对象
已经在 `common.ignore` 里排掉了，index 本身不能排：没有它 git 会认为
整个工作区都是未跟踪状态。

## 冲突

Mutagen 的默认模式是 `two-way-safe`：**两端都改过同一个文件时，它什么都不做**，
把冲突标出来等你决定。不会有文件被静默覆盖或删除。

```sh
rcsync conflicts                  # 看哪些文件卡住了
rcsync resolve myapp alpha    # 以本机为准
rcsync resolve myapp beta     # 以 vps 为准
```

`resolve` 做的事是：把输的那一端的那几个路径删掉，然后触发一次同步，
让赢的那端的版本重新长回去。执行前会把要删的路径列出来让你确认。

`.git/index` 冲突一般选哪端都行 —— 选完在那一端 `git status` 一下，
index 会自己重建到正确状态。

## 常用操作

```sh
rcsync status            # 一张表看全部目标：会话名、状态、冲突数
rcsync up                # 建立/恢复同步（已在同步的会跳过）
rcsync up myapp      # 只处理一个目标
rcsync up code           # 只处理代码类目标（还有 sessions / skills）
rcsync flush             # 别等 watch，立刻同步一轮并等它完成
rcsync pause / resume    # 临时停掉（比如要跑一个会产生几万个临时文件的构建）
rcsync monitor myapp # 实时刷新单个目标
rcsync down              # 终止同步（只停同步，两端文件都保持原样）
```

`up` 在新建同步前会把要建的路径对列出来让你确认。首次同步是**双向合并**：
两端各有的文件都会传给对方，两端都有但内容不同的进冲突，不会有文件被删掉。
第一次对一个已经两端都有内容的目录起同步时，看清楚这个列表再回车。

## 性能

- 大仓库第一次扫描很慢（14GB / 15 万文件的 SDK 要几分钟），之后是增量，感觉不到。
- 会产生几千个临时文件的构建（`make`、`cargo build`）最好先 `rcsync pause`，
  否则 Mutagen 会忙着同步一堆马上就要被删的东西。或者直接把产物目录加进忽略。
- 传输默认走 zstd 压缩，这在高延迟窄带链路上是净赚。
