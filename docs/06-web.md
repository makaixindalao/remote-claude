# rcweb —— 浏览器里用 VPS 上的 Claude

`scc` 解决的是「人坐在 Mac 前」。rcweb 解决的是「人不在 Mac 前」：手机、平板、别人的电脑，
开个浏览器就能接着用 VPS 上的 Claude，看它在干什么、批准它要跑的命令、接回终端。

它是跑在 VPS 上的一个 Go 程序（`server/`），前端（`server/ui/`，React + shadcn/ui）编进同一个二进制，
部署就是拷一个文件加一个 systemd 服务。

## 部署

```sh
cp server/.env.example server/.env   # 改 RCWEB_PASSWORD（登录密码）
rcweb deploy                         # 构建前端 + Linux 二进制 → 上传 → systemd 服务 → 健康检查
```

`deploy` 需要本机有 `go` 和 `npm`；VPS 上什么都不用装。主机、项目根目录、项目列表、claude 路径
都从 `~/.config/remote-claude/config` 推导，写进 VPS 的 `/etc/rcweb.env`（0600）；`server/.env`
里写了的键以它为准。改密码 = 改 `server/.env` 再 `rcweb deploy`，所有已登录的浏览器会一起失效。

`deploy.sh` 部署新 VPS 时也会顺带做这一步，默认开到公网 IP（见下面「怎么打开」），`--no-public` 则只监听回环。

其余：`rcweb status | logs [-f] | restart | stop | start | uninstall`。

## 怎么打开

监听地址由 `server/.env` 的 `RCWEB_LISTEN` 决定。能登录的人就能在 VPS 上以 root 跑命令，开到公网前想清楚。

| 方式 | 做法 |
|---|---|
| 公网 IP 直连（`deploy.sh` 默认） | `RCWEB_LISTEN=0.0.0.0:7681`，浏览器开 `http://<VPS IP>:7681`。`rcweb deploy` 会放行 VPS 上开着的 ufw / firewalld，再从本机连一次确认；云厂商的安全组要自己去控制台放行 TCP 7681 |
| 临时隧道 | `RCWEB_LISTEN=127.0.0.1:7681`，`rcweb tunnel`，浏览器开 `http://localhost:7681` |
| Tailscale | 同上只监听回环，VPS 上执行一次 `tailscale serve --bg 7681`；手机 / 其他电脑装上 Tailscale，开 `https://<机器名>.<tailnet>.ts.net` |

公网直连走的是**明文 HTTP**：密码和登录 cookie 在路上都看得见，别在公共 Wi-Fi 上用；
要加密就用 Tailscale（自带 HTTPS），或者前面再挂一层 HTTPS 反向代理。
`deploy.sh` 在公网模式下要求登录密码至少 12 位（自动生成的是 20 位）。

安全上还有几层：密码登录换 HttpOnly + SameSite=Strict 的 cookie（签名 key 由密码派生，服务端不存会话）；
会改状态的请求和所有 WebSocket 都校验 Origin；同一来源 IP 密码连错 10 次冷却 10 分钟（按 IP 计，
公网上别人乱试不会把你锁在外面；IPv6 按 /64 算）；子进程（claude、tmux、shell）
的环境里摘掉了 `RCWEB_*`，密码不会漏进 Claude 跑的命令。

## 三种用法

**网页对话**：页面里直接和 Claude 聊。背后是一个常驻的
`claude -p --input-format stream-json --output-format stream-json --permission-prompt-tool stdio`：

- 进程和页面解耦。关页面、手机锁屏、网断了，活照跑；重新打开先收一份快照再接增量（顶替 tmux + mosh）。
- 工具权限在页面上点「允许 / 总是允许 / 拒绝」（Claude 的提问、计划审批也一样），
  所以网页对话默认不需要 `--dangerously-skip-permissions`。
- 输入框打 `/` 弹出命令菜单，内容是 CLI 自己报的全部命令（内置、skills、自定义命令）；
  另有网页自己的 `/exit`、`/resume`、`/terminal`。
- 输入框下面切模型 / effort / 权限模式（`default` `acceptEdits` `plan` `auto` `bypassPermissions`），
  当场生效。新对话用什么在「设置 → 对话」里定，每一项都可以是「和上次一致」。
- 三样都显示具体的值，不显示「Default」「默认 effort」：
  - 模型写具体是哪个（`Opus 5.5 (1M)`、`Sonnet 5`）。Claude Code 推荐的那个（`default`）也写成它现在指向的模型。
  - 对话里以**实际回复的模型**为准（API 应答里的 `model`）。选的是 Opus 5.5、回复却来自 Opus 5 / 4.8 时，
    模型那一格标黄、带 ⚠，悬停或点开下拉能看到「选的是 …，最近一条回复实际来自 …」；页头也写实际的模型。
    会话记录里模型换了的地方插一行「下面由 Opus 4.8 回复（之前是 Opus 5.5）」。
  - effort 没选时显示这个模型实际用的那一档（CLI 的 `get_settings` 报的，Opus 5.5 是 medium、Sonnet 5 是 high，
    也受 settings.json 里 `effortLevel` / `modelSettings` 影响）。新对话还没起进程时，各模型的这一档是
    `/api/catalog` 起的短命进程挨个 `set_model` + `get_settings` 问出来的，不花 token。
  - 权限模式 `default` 写成「default · 每次都问」，免得读成「默认」。
- 和 TUI 一样：淡色的下一句建议按 Tab 采纳，↑ / ↓ 翻输入历史（就是 `~/.claude/history.jsonl`，
  网页发的消息也会记进去）。
- 侧栏状态：运行中 / 等你回答 / 后台运行中 / 已完成（看过变空闲）/ 出错。
  「后台运行中」是这一轮回完了、但后台命令或后台子代理还在跑（灰色的光芒，对话里列出在跑的任务）；
  它们跑完 Claude 会自己接着处理结果，这时又变回运行中，处理完才算完成。
  运行中的标志是一颗 SVG 画的光芒，不用 ✻✳ 这类字符（手机上字体不同，✳ 还会变成 emoji）。
- Claude 问你问题（AskUserQuestion）时，选项是一颗颗橙色的「鹅卵石」按钮：单选点一下，多选可勾多个，
  「其他」自己写；卡片有焦点时按 1–9 选、Enter 提交。会话记录里这次提问也显示成同样的按钮，
  标出当时选了哪个（合并模式下收成一行「问了你 2 个问题 「蓝色」「Web, iOS」」，点开看全部选项）。
- 右上角三个开关：思考展开；工具调用「合并 / 半开 / 全开」（合并 = 连续调用收成「读了 3 个文件」一行；
  半开 = 这一行点开了的样子，下面一个调用一行，参数和输出还收着；全开 = 每个调用都展开，输出全部显示）；
  右侧终端。进度行带已用时间「✻ 30s · 思考中…」。
- 回复里的代码块是独立卡片（语言名 + 复制按钮 + 高亮），```mermaid 画成流程图；
  提到的文件路径（`server/usage.go:38` 这种）可以点，在右半屏打开，带行号、高亮、跳到那一行。
  工具卡片里 Read / Edit / Write 的路径也能点。能打开的文件限制在 `RCWEB_ROOT` 下。
- 底部常驻一行状态栏，照本地 statusline 的样子：`上下文 14% 28k/200k │ 5h 21% ↻38m │ 7d 4% ↻6d22h`。
  上下文是每轮结束后问 CLI 要的（和 `/context` 同源）；5h / 7d 取对话里 CLI 推来的 `rate_limit_event`
  和 `~/.claude.json` 里 Claude Code 自己缓存的额度中较新的一个，不为这个额外发任何请求。
- 压缩上下文（`/compact` 或自动压缩）时进度行和状态栏显示「正在压缩上下文…」进度条，
  压缩完留一行「上下文已压缩：120k → 18k tokens · 12s」。

**网页终端**：xterm.js 接 `tmux attach`，和 Mac 上 `scc` 接回的是同一个 `cc-<目录名>` 会话。
这里跑的是完整 TUI，所有交互命令都能用。对话页右上角的终端开关会在右半屏开一个 shell
（`sh-<目录名>`，关掉面板它也还在），配色跟页面主题走。

**会话记录**：按项目浏览 `~/.claude/projects/` 下的全部会话（含 worktree 里的），
在输入框里打字就接着这个会话在网页里继续，也可以「终端里继续」。

## 文件和图片

所有页面的输入框都能带文件，三种加法：

- **粘贴**：截图、网页上「复制图片」、在 Finder / 资源管理器里复制的文件，⌘V / Ctrl+V。焦点不在输入框里也行。
  从表格、网页、Word 里复制的一段照常粘成文字（浏览器会顺带塞一张渲染出来的图，不当附件）。
- **拖进来**：拖到窗口任何地方，松手就加上。文件夹不行，先打个压缩包。
- **回形针**：输入框左下角。手机上从这里选照片、拍照、选文件。

选中就开始传，缩略图上有进度，可以接着打字；按发送时没传完的等它传完再发，传失败的点「重试」或者去掉。
可以只发附件不打字。一条消息最多 20 个，单个 100 MB 以内。

传上去的存在 VPS 的 `~/.local/state/rcweb/uploads/<id>/<原文件名>`，30 天后自动删；发出去之前在输入框里去掉的当场删。
发给 CLI 的消息末尾附一段 `<attachments>`，列出每个文件在服务器上的路径，要用它自己去读（PDF、日志、代码、压缩包都行）：

| | 图片 | 其它文件 |
|---|---|---|
| Claude | 放进消息直接给模型看（image 块） | 读路径。上传目录用 `--add-dir` 加进去了，读的时候不问你 |
| Codex | `localImage`，直接看 | 读路径 |
| Grok | 它不收图片（ACP 的 `promptCapabilities.image` 是 false），只给路径，要看自己用 Read 打开 | 读路径 |

- 模型看的图有上限：单张 base64 后 5 MB 以内、长边 2000 以内（对话里图一多 API 就按这个卡）。超了，或者是模型不认、
  浏览器能解开的格式（HEIC、BMP、AVIF…），浏览器顺带缩一份给模型看，存成 `<id>.vision.png|jpg`。
  给 CLI 的路径还是原图，「把这张图放进 public/」拿到的是原文件。浏览器也解不开的（比如 Chrome 里的 HEIC）只能当普通文件。
- 对话和会话记录里，附件显示在那条消息上面：图片是缩略图，文件是一张小卡片，点开是原文件（图片、纯文本在新标签页里看，
  别的下载；整页沙箱化，传上来的 HTML / SVG 跑不了脚本）。过了 30 天只剩名字，不过 Claude 会话记录里的图本身还在。
- 粘贴的截图浏览器都叫 `image.png`，这里按时间改名成 `paste-20260924-153012.png`。

## Codex 和 Grok

VPS 上装了 `codex`（OpenAI Codex CLI）或 `grok`（Grok Build），上面三种用法对它们同样成立，
没装的在页面上不出现。新对话在输入框下面最左边选 Claude / Codex / Grok（记住上次的），
续会话、终端里继续则跟着那条会话走。列表里 Codex / Grok 的会话带个小标签，项目页可以按 CLI 筛。

| | 会话记录从哪读 | 网页对话背后 | 终端里跑 |
|---|---|---|---|
| Codex | `~/.codex/sessions/年/月/日/rollout-*.jsonl`，按记录里的 cwd 归到项目 | `codex app-server`（JSON-RPC） | `codex [resume <ID>]`，会话名 `cx-<目录名>` |
| Grok | `~/.grok/sessions/<编码过的 cwd>/<ID>/updates.jsonl` | `grok agent stdio`（ACP） | `grok [--resume <ID>]`，会话名 `gk-<目录名>` |

- 在项目子目录里开的 Codex / Grok 会话也算这个项目；子代理的内部线程不单列。
- 工具调用按 Claude 的样子显示（命令是 Bash、改文件是 Edit / Patch 的 diff），批准卡片也是同一张。
- 权限模式各用各的名字：Codex 是 `read-only` / `auto`（默认）/ `full-access` 三档（审批 × 沙箱，同它 TUI 的 `/approvals`）；
  Grok 是 `default`（问）/ `auto` / `bypassPermissions`（always-approve），其中 auto 只能在开对话时选，
  中途切 always-approve 会发一条 `/always-approve on|off`。
- 模型 / effort 列表由 CLI 自己报（Codex 的 `model/list`、Grok 的 initialize），换模型、effort 当场生效或从下一条消息起生效。
- Codex 的「拒绝」：它不给「拒绝但接着干」这个选项时，拒绝就是停掉这一轮（和它 TUI 一样）。
- 发给 Codex 的消息记进 `~/.codex/history.jsonl`；删除挪进 `~/.codex/.rcweb-trash/`、`~/.grok/.rcweb-trash/`。
  rcsync 只同步 Claude 的会话，Codex / Grok 的记录只在 VPS 上。
- 网页终端开关 `RCWEB_TERM_SKIP_PERMISSIONS` 对三者都生效：Codex 补 `--dangerously-bypass-approvals-and-sandbox`，
  Grok 补 `--always-approve`。

## 改动记录

网页对话和会话记录里，每轮末尾有一张卡片列出这一轮改了哪些文件、各 `+N -M` 行
（Claude 的 Edit / Write、Codex 的 Patch、Grok 的改文件都算；没跑完的、出错的不算）。
点一个文件，右半屏打开 diff：

- 默认是**工作区**（标题「main → 工作区」）：`git diff HEAD`，未提交的全部改动，含没跟踪的新文件。
  点的那个文件展开并滚过去，其余收着，点文件名展开。没改的长段收成「N 行未改动」，点开能一直展开到头。
- 标题上的下拉切到**这一轮**：只看这一轮对话里工具改了什么。点的文件在工作区里已经没有改动（提交过了）、
  或者目录不是 git 仓库时，自动用这个。
- 每处改动上下各带 3 行没改的，带行号。Claude 的 diff 用它自己给的；只有前后片段的（Grok 之类），
  在现在的文件里找到那段补上行号和上下文，找不到（之后又改过）就只显示片段。工具卡片里的改动也是这样显示。
- 只看 `RCWEB_ROOT` 下的仓库；仓库根目录在 `RCWEB_ROOT` 外面的不给看。

## 设置

侧栏底部「设置」，分五页：**外观**只记在当前浏览器里；**对话**、**通知**、**reclaude**、**Remote Control** 存在 VPS 上
（`~/.local/state/rcweb/settings.json`），所有浏览器看到的是同一份。

### 外观

`#/settings`：主题、字体、字号，改了当场生效（手机和电脑各设各的）。

- **界面字体**：Geist（默认）、Inter，或者系统字体（苹方 / 微软雅黑 / 思源黑体）、衬线（宋体 / 思源宋体）。
- **代码字体**：代码块、工具输出、网页终端共用。系统等宽（默认）、JetBrains Mono、Fira Code、Geist Mono。
- 两处都能选「自定义」，填本机装的字体名，逗号隔开可以写好几个。浏览器里找不到会当场提示；
  Chrome / Edge 桌面版在 HTTPS 或 localhost 下还能「列出本机字体」来挑。Safari 不让网页用用户自己装的字体。
- **正文字号** 12–20px（默认 14），界面上其余的字按同样比例缩放，间距和图标不变；**终端字号**单独调（默认 13，触屏 12）。
- 内置的几款只编进了拉丁字形，中文照样用系统字体；选中了才下载。

### 对话

`#/settings/chat`：新对话一开始用什么模型、思考（effort）、权限模式，Claude / Codex / Grok 各设各的。

- 每一项都可以选「和上次一致」：跟着上次在输入框里选的走（也存在 VPS 上，手机和电脑是同一个「上次」）。
- effort 还能选「跟着模型」：不指定，各模型用自己的那一档，下拉里列着每个模型具体是哪档。
- 每组标题下面写着按现在的设置、新对话实际会用的那一套。

### 通知

`#/settings/notify`：对话**完成**（后台任务也跑完了）、**等你回答**（批准命令、回答问题、审批计划）、**出错**时提醒你，
三种各自能关。通知是服务端发的，页面关了、手机锁屏也收得到；有人正开着那个对话的页面（在前台）时不发。
标签页标题和图标上的「●」一直都有。

- **这个浏览器**：Web Push。浏览器只在 https 或 localhost 下给通知 —— 用 `http://<VPS IP>:7681` 打开的话，
  手机、电脑都开不了，要么换 Tailscale（自带 https），要么用下面的 App 推送。
  安卓 Chrome 直接开；iPhone / iPad 要先在 Safari 里「分享 → 添加到主屏幕」，从主屏幕打开再开（iOS 16.4 起）。
  「试一下」是让服务器真发一条，顺便验证 VPS 连得上浏览器的推送服务（FCM / Mozilla / Apple）。
  VAPID 密钥和订阅存在 `~/.local/state/rcweb/push.json`。
- **推送到手机 App**：服务器直接发，http 打开的 rcweb 也能用。
  [ntfy](https://ntfy.sh)（安卓 / iPhone）填主题地址，如 `https://ntfy.sh/一个别人猜不到的名字`（自建的 ntfy 也行）；
  [Bark](https://bark.day.app)（iPhone）填它首页的地址 `https://api.day.app/<key>`。点通知打开保存时地址栏里的那个网址。
  通知里有对话标题和一两句内容，会经过 ntfy / Bark 的服务器。
- 浏览器订阅不了推送（没有 PushManager 的老浏览器）时退到页面自己弹，得开着页面才行。

### reclaude

`#/settings/reclaude`。VPS 上装了 reclaude（claude 的外壳：本地 daemon 代理流量、管账号和组织）
才能用，按 `RCWEB_RECLAUDE_BIN`、PATH、`~/.local/bin/reclaude` 的顺序找。

- **用 reclaude 代替 claude**：打开后，网页对话、网页终端、输入框的命令菜单、Remote Control 里起 claude 的地方都换成
  `reclaude <同样的参数>`（reclaude 不认识的参数原样转给 claude）。只影响之后新开的，已经在跑的对话和终端不变。
- **状态**：`reclaude status` 的 daemon、网关、claude 路径，「全部输出」看原文。
- **组织**：`reclaude org` 列出的组织，点「切换」= `reclaude org use <ID>`。列不出来多半是还没登录：
  到网页终端里跑一次 `reclaude login`（设备码登录，要在浏览器里授权）。
- **网关**：当前用的（`config gateway current`）；「测速」把所有网关测一遍、按耗时排好，点「使用」=
  `config gateway set`；测不通时 reclaude 不让设，报错里有「仍然使用」（`--force`）。也能填自定义地址先测再用；
  「恢复默认」= `config gateway reset`。
- reclaude 的 `config` 子命令会先同步一遍配置，慢的时候要四五十秒，所以网关那块可能转一会儿，别的几块不受影响。
  这类命令一次只跑一条，页面关了也会跑完。

### Remote Control

`#/settings/rc`。打开后在这台机器上常驻 `claude remote-control`（`claude rc`），手机上的 Claude App、
claude.ai/code 就能连过来开会话、在 VPS 上干活。

- rcweb 把守护脚本写到 `~/.local/state/rcweb/claude-rc.sh`，放进 tmux 会话 `claude-rc` 里跑。
  所以重启 rcweb 不影响它；rcweb 启动时发现开关开着、会话却不在（比如 VPS 重启过），会自己拉起来。关掉开关 = 结束这个会话。
- **claude rc 的参数**：在哪个项目目录里跑、`--name`、`--permission-mode`（它开出来的会话用；root 下选
  `bypassPermissions` 会像 scc 一样带上 `IS_SANDBOX=1`）、`--spawn`、`--capacity`。默认值不写进命令行，跟着 claude 自己的默认走。
- **守护脚本**：「退出后自动重启」开着时，断线、出错退出后隔几秒（默认 10）再起；关着就停在那儿，窗格留着，
  页面上能看到它最后的输出和退出码。
- 改了参数点「保存并重启」才作用到正在跑的这个（会断开现在连着的远程会话）。reclaude 开关同理：起的是当时选的程序。
- 页面下方是 `claude-rc` 窗格最后几十行，里面的会话链接能直接点。第一次跑、换了目录时它可能在终端里问问题
  （开不开 Remote Control、信不信任这个目录），输出停住了就点「终端里看」进去答。

## 归档和删除

- 归档只是 rcweb 记的标记，存在 VPS 的 `~/.local/state/rcweb/archived.json`，不碰会话文件。
- 删除是把会话文件（和同名附属目录）挪进 `~/.claude/.rcweb-trash/<时间>-<ID>/`，旁边的 `meta.json`
  记着原位置，挪回去就恢复了。**rcsync 会把删除同步到 Mac。** 正在网页里跑的会话不让删。

## 注意

- 同一个会话别同时在 TUI 和网页对话里续写，两个进程会往同一份记录里写。网页里「到终端里继续」
  会先结束网页这边的进程。
- 网页对话没人看、也没在干活，闲置 12 小时后结束进程（`RCWEB_CHAT_IDLE` 可调）；会话记录还在，随时能续。
- 重启 rcweb 会结束所有网页对话的进程（记录都在），tmux 会话不受影响
  （新建 tmux 时套了 `systemd-run --scope`，不在 rcweb 服务的 cgroup 里）。

## 本地开发

```sh
rcweb dev                      # 本机跑一份，用本地路径（RC_LOCAL_ROOT）
cd server/ui && npm run dev    # 另开一个：前端热更新，接口代理到上面那个
cd server && go test ./...
```
