# rcweb 需求清单

用户提过的需求逐条记在这里，做完打勾，避免遗漏。「验证」一栏写实际怎么确认过的。

> 协作注意：另一个会话正在给 server/ 加 Codex / Grok 多 agent 支持（agents.go、codex.go、grok.go、
> jsonrpc.go，chat.go 改成按 driver 分）。标了「等服务端」的几项要改 chat.go 的事件处理，
> 等那边编译通过、结构稳定后再合进去，别两边同时改同一个文件。

## 已完成

- [x] Go 服务端单文件部署（`rcweb deploy`），只监听 127.0.0.1，经 Tailscale / SSH 隧道访问 —— 本机跑通；**还没部署到 VPS**
- [x] 密码登录：`RCWEB_PASSWORD` 走 env，写在 `server/.env`（已 gitignore） —— curl + 浏览器验证
- [x] 前端用 shadcn/ui（React + Vite + Tailwind，编进 Go 二进制）
- [x] 网页对话：headless stream-json、权限卡片（允许 / 总是允许 / 拒绝、AskUserQuestion、计划审批）、断线重连快照 —— 浏览器实测
- [x] 网页终端：xterm.js ↔ tmux，和 scc 共用 `cc-<目录名>` 会话；中文、dim、24 位色 —— 浏览器实测
- [x] 所有页面都有输入框（新对话页、项目页、会话记录页直接打字就开始 / 续会话） —— 浏览器实测
- [x] 全部斜杠命令：`/` 弹菜单，内容是 CLI initialize 报的完整命令表（80 个，带说明和参数提示）；网页补 `/exit` `/resume` `/terminal` —— 浏览器实测 /context
- [x] 模型默认 `default`（Claude Code 推荐的最新模型），记住上次的模型、effort、权限模式；权限模式显示英文原名；运行中可切换 —— WebSocket 脚本实测 set_model / set_mode
- [x] 侧栏项目可展开显示会话；会话归档 / 删除（回收站 `~/.claude/.rcweb-trash/`）；项目页按活跃 / 已归档 / 全部筛选 —— 浏览器 + API 实测
- [x] Claude 橙色主题 + 暖色调，深色模式 —— 截图确认
- [x] 回复 / 思考动画：Claude 星形 spinner、扫光文字、新消息淡入
- [x] 侧栏会话状态：运行中 / 等你回答 / 已完成（看过转空闲）/ 出错 —— WebSocket 脚本实测状态流转
- [x] 暗色建议输入（CLI 的 prompt_suggestion，Tab 采纳）—— 协议实测（两轮后出现）；**页面上没亲眼看到过一次**
- [x] ↑ / ↓ 翻输入历史（读写 `~/.claude/history.jsonl`，和 TUI 共用）
- [x] 右上角开关：思考展开、工具调用三档、右侧终端面板（配色跟主题、Tab 补全、tmux 保活）—— 浏览器实测
- [x] 工具调用三档改为「合并 / 半开 / 全开」：合并 = 连续调用合成「读了 2 个文件」一行 —— 浏览器实测
- [x] 进度行带已用时间「✻ 30s · 思考中…」（按本轮用户消息的时间戳算）
- [x] 暗色建议输入 —— 浏览器里亲眼看到（输入框里暗色「take a screenshot」+ Tab 采纳建议）
- [x] 代码块独立显示（卡片、语言名）+ 一键复制 + highlight.js 高亮（按需加载）—— 浏览器实测
- [x] mermaid 流程图（按需加载，跟随深浅色，语法错显示原文）—— 浏览器实测。
  顺带修了：`//go:embed` 默认跳过 `_` 开头的文件，mermaid 的 `_baseUniq-xxx.js` chunk 404 回落成 index.html，改成 `all:ui/dist`
- [x] 文件名高亮可点击，右侧打开：面包屑、行号、高亮、跳到 `:行号`；工具卡片里 Read / Edit / Write 的路径也能点；
  `GET /api/file` 限制在 RCWEB_ROOT 下（EvalSymlinks 防逃逸，1MB 上限，二进制识别）—— 浏览器实测 + curl 验证 /etc/passwd 403
- [x] compact 进度条：压缩中进度行显示「正在压缩上下文…」+ 不定长进度条（底部状态栏同步）；压缩完留一行「上下文已压缩：Xk → Yk tokens · Ns」
  —— **还没真触发过一次压缩**（要把上下文撑满才会有）
- [x] 常驻状态栏（页面底部）：`上下文 ▰▱ 14% 28k/200k │ 5h ▰▱ 21% ↻38m │ 7d ▱ 4% ↻6d22h`，零额外网络请求：
  - 上下文：每轮结束后 control_request `get_context_usage`（和 /context 同源），只在网页对话页显示
  - 5h / 7d：对话里的 `rate_limit_event` 和 `~/.claude.json` 的 `cachedUsageUtilization` 取较新的 —— 浏览器实测

- [x] **AskUserQuestion 选项按钮 + 查看历史选择**（2026-09-23 提），**橙色鹅卵石风格**（参考 claude 网页）
  - 等你回答：选项是圆润的胶囊 / 卵石按钮，没选暖沙色、选中实心橙色；单选 / 多选；「其他」自己写；
    卡片出现时如果输入框是空的就拿焦点，按 1–9 选、Enter 提交；多个问题时竖线标出数字键作用在哪题
  - 会话记录：同样的按钮（只读）标出当时选了哪个，「其他」写的也显示；合并模式收成一行带答案摘要。
    答案从 tool_result 正文按问题原文解析（两种措辞都认，答案里有引号逗号也不切错，bun 实测）；
    liteEntries 不精简 AskUserQuestion（af 会话加的）
  - 浏览器实测：数字键选择、自填「其他」、提交后 Claude 复述正确；记录页深 / 浅色都看过
- [x] 文档：docs/06-web.md 补上以上新功能
- [x] 修 `npm run dev` 代理改写 Host 导致写接口「来源校验失败」：vite proxy `changeOrigin: false, xfwd: true`
- [x] **文件和图片**（2026-09-24 提）：输入框里粘贴、拖进窗口、点回形针选；选中就传（进度、失败重试、去掉），发的时候等传完
  - Claude 的图片走 image 块、Codex 走 localImage、Grok 不收图片只给路径；全部附件的路径列在正文末尾的 `<attachments>` 里，
    Claude 加了 `--add-dir <上传目录>`，读附件不用批
  - 太大 / 模型不认的图浏览器缩一份给模型看（长边 2000、3.75MB），原图保留；30 天清理
  - 验证：Go 测试（上传、缩图收 / 拒、沙箱化下载、claude 消息里的 image 块）；三个 CLI 各真发一条（图片颜色、文本文件里的暗号都答对，
    Claude / Grok 读上传目录没弹权限）；headless Chromium 实测粘贴、拖入、回形针、超尺寸图缩图、上传失败重试、表格富文本不当附件、
    会话记录回看、深浅色、手机宽度 —— **真机（iPhone 粘贴、选照片）还没试**
  - 顺带修了：页面 CSP 的 img-src 没放 `blob:`，本地文件的缩略图和缩图时的解码都被挡

## 进行中 / 待做

- [ ] 部署到 VPS（`rcweb deploy`，或 deploy.sh 顺带）—— **动 VPS 之前先问用户**
