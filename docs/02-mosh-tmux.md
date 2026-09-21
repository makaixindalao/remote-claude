# 防断连

三层，缺一层就会在某个场景下掉链子：

| 层 | 挡住的是 | 断了会怎样 |
|---|---|---|
| SSH 保活 + 复用 | NAT 表项老化、短暂丢包 | 连接悄悄僵死，敲什么都没反应 |
| mosh | 换网、睡眠、IP 变更 | SSH 直接断，当前窗口的输出全丢 |
| tmux | 上面两层都失败 | 进程还在，重新接回去就行 |

## 第一层：~/.ssh/config

```
Host vps-64
  # 每 15s 一次心跳，连续 8 次(2分钟)没响应才判死
  ServerAliveInterval 15
  ServerAliveCountMax 8
  TCPKeepAlive yes

  # 连接复用：多个终端共享一条 TCP，第二条起近乎瞬时
  ControlMaster auto
  ControlPath ~/.ssh/cm/%r@%h:%p
  ControlPersist 30m

  ConnectTimeout 20
  ConnectionAttempts 3
  Compression yes
```

心跳更密是为了防 NAT 表项老化，容忍次数更多是为了扛住网络抖动 —— 这两个要一起调，
只调一个会变成"稍微抖一下就判死"或者"死了两分钟才发现"。

复用不只是省握手：`sshv` 每次列会话、验目录都要开一条 SSH，没有 `ControlMaster`
的话每次敲 `sshv` 都要多付一个完整握手的时间。

`ControlPath` 指向的 `~/.ssh/cm` 目录必须存在，`install.sh` 会建。

## 第二层：mosh

mosh 走 UDP，自己维护"状态同步"而不是"字节流"：
IP 变了、睡了一觉、换了个 Wi-Fi，它自己会重连，终端内容直接续上。
外加本地回显预测 —— 231ms 的链路上打字不再需要等一个往返。

`sshv` 默认 `--predict always`（而不是 mosh 默认的 `adaptive`）：
固定高延迟下 adaptive 会在"没把握"时放弃预测，always 让预测始终可见
（带下划线，确认后下划线消失）。

**需要 UDP 60000-61000 放行。** VPS 的安全组/防火墙没开这段的话 mosh 连不上，
这时用 `sshv --ssh` / `scc --ssh` 退回 SSH。

mosh 的代价：它丢弃 SGR 2（dim 暗色）属性。Claude 输入框里的灰色建议文本
因此显示成正常亮度。介意就用 `--ssh`，代价是失去本地回显预测。

## 第三层：tmux

前两层都是"尽量不断"，tmux 是"断了也不要紧"：进程跑在 tmux 里，
客户端断开不影响它，接回去继续看。

`sshv` 用的是 `tmux new -A -D -s <name>`：
- `-A` 存在就接回、不存在就创建，一条命令幂等
- `-D` 踢掉断线残留的僵尸 client。不加的话窗口尺寸会被拉成"所有 client 中最小的那个"，
  表现为莫名其妙的窄窗口

## sshv

```sh
sshv                 新建一个自动编号的会话并连接
sshv add <name>      新建指定名字的会话
sshv <name>          连接已有会话（不存在不会自动建，防止拼错名字堆垃圾会话）
sshv -c              续连上次那个
sshv ls              列出远端所有会话
sshv rm <name>       删掉一个

--host/-H <host>     指定主机（默认取配置里的 RC_HOST）
--ssh                走 SSH 而非 mosh（需要端口转发、或 UDP 被封时）
--bare               不套 tmux，纯 mosh（预测效果最好，但断开即丢）
--cd <path>          指定远端起始目录
--no-cd              不做路径映射
--run <cmd>          在会话里跑指定命令而非默认 shell
--new                同名会话已存在时另开一个（自动加 -2 -3 后缀）
--attach/-a          强制接回，即使本机另有窗口连着它
```

### 两个不显然的设计

**路径映射只在新建会话时生效。** 接回已有会话时 tmux 会忽略 `-c`，
保留它原来的现场 —— 这是对的：你接回去是想看它现在在哪，不是把它拽走。

**"谁正连着"是在本机判断的，不是问远端。** mosh 断线后 mosh-server 仍然持有
tmux client，远端照样显示 `attached=1`，分不出"你正用着"和"你断线了"。
而本机那个 `sshv` 进程是否还活着，恰好就是这两者的区别本身。
所以 `sshv` 在 `~/.config/sshv/active` 里登记 PID，靠 `kill -0` 判断有效性：
同名会话本机已有窗口连着时自动另开一个，而不是用 `-D` 把那个窗口踢下线。
