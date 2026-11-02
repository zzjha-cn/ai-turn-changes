# Codex Rewind 设计与实施文档

> 文档状态：设计基线，尚未实现。
>
> 目标形态：独立 Go 项目，命令名暂定 `cxr`。
>
> 当前验证环境：Codex CLI `0.164.0`，macOS。
>
> 编写日期：2026-10-31。

## 1. 文档目的

本文档定义 Codex CLI 的 turn 级回退工具设计与实施计划。工具需要同时回退：

- Codex 对话历史到指定 turn 之前或之后；
- 工作区文件到该 turn 对应的状态；
- 两者之间的映射、事务、冲突检测和数据清理。

本文档不是 Codex 源码改造方案，也不是 PTY 屏幕抓取方案。所有能力都建立在 Codex 已暴露的 lifecycle hooks、app-server 协议和本地文件系统之上。

## 2. 目标与非目标

### 2.1 目标

1. 在项目根目录维护自包含的 `.codex-rewind/` 数据目录。
2. 每个 Codex session 独立保存最多 10 个终态 turn 的回退窗口。
3. 每个 turn 同时包含稳定 ID 和人类可读摘要，支持终端选择。
4. 在 turn 边界自动采集工作区快照，不要求用户手动开始/结束。
5. 支持 `rewind --before`、`rewind --through` 和 `rewind --last`。
6. 默认使用软 fork：保留原对话和原历史，只创建新 thread。
7. 提供 30 天数据清理命令，清理旧 session 和孤儿快照。
8. 不启动常驻进程，不使用 PTY，不修改 Codex 源码。
9. 文件存储不使用 SQLite；元数据使用有限数量、可重建的 JSON 文件。

### 2.2 非目标

1. 不在 Codex TUI 运行期间直接注入按键或重写当前 TUI 状态。
2. 不解析 `transcript_path`。该格式不是稳定接口。
3. 不恢复数据库、网络请求、容器、后台进程等外部副作用。
4. 不默认恢复 Git index、ignored 文件或 `.git/` 内容。
5. 不把快照写入用户仓库的 `.git/objects`。
6. 不提供跨机器同步。
7. 不默认执行不可逆的 `thread/revert`。

### 2.3 产品边界

不使用 PTY 时，无法在正在运行的 TUI 内部直接跳回历史。产品的准确语义是：

```text
选择目标 turn
  -> 从该 turn 分叉对话
  -> 恢复对应工作区
  -> 退出旧 Codex 会话
  -> 用新 thread 继续
```

回退命令必须在目标 session 没有活动 turn 时执行。

## 3. 已验证的技术前提

### 3.1 Lifecycle hooks

Codex hooks 为每个命令 hook 在 stdin 提供一个 JSON 对象，常用字段包括 `session_id`、`turn_id`、`transcript_path`、`cwd`、`hook_event_name`、`model` 和 `permission_mode`。

与本方案直接相关的事件：

- `SessionStart`
- `UserPromptSubmit`
- `PreToolUse`
- `PostToolUse`
- `Stop`
- `Interrupt`
- `SessionEnd`

其中：

- `UserPromptSubmit` 发生在用户 prompt 即将发送给模型之前；
- `Stop` 发生在 turn 完成之后；
- `Interrupt` 的默认超时只有 1 秒，最大可配置为 3 秒；
- `Stop` 事件提供 `last_assistant_message`，可用于人类可读摘要；
- `transcript_path` 不能被当作稳定数据格式解析。

参考：<https://learn.chatgpt.com/docs/hooks>

### 3.2 Codex app-server

app-server 支持 stdio JSONL 协议。每次 rewind 可以临时启动：

```bash
codex app-server --listen stdio://
```

完成初始化、请求和响应后关闭 stdin 并退出，不需要常驻 daemon。

与本方案相关的协议方法：

- `thread/list`
- `thread/turns/list`
- `thread/fork`
- `thread/revert`，仅作为未来显式 in-place 选项

对话回退的关键参数：

- `thread/fork.beforeTurnId`：复制该 turn 之前的历史；
- `thread/fork.lastTurnId`：复制到该 turn 为止的历史；
- `thread/fork.excludeTurns`：只返回 thread 元数据，避免加载完整历史。

注意：`beforeTurnId` 和 `lastTurnId` 属于实验能力。客户端必须在 `initialize` 中声明 `capabilities.experimentalApi = true`。

参考：<https://learn.chatgpt.com/docs/app-server>

### 3.3 本机验证结果

在 Codex CLI `0.164.0` 上完成过最小探测：

- `initialize` + `thread/list` 约 0.3 秒；
- 加载历史并执行内存 `thread/fork` 约 4 秒；
- 请求结束并关闭进程后没有 daemon 残留；
- 未声明 `experimentalApi` 时，`beforeTurnId` 会被拒绝。

这证明“临时 app-server + 无常驻服务”的路线可行，但不代表未来版本永远兼容。实现必须做版本和能力探测。

### 3.4 Git 的职责

Git 和 `.gitignore` 继续负责仓库根目录、tracked 文件、未 ignore 的 untracked 文件、ignore 过滤和变更检测的快速信号。

Git 不负责 turn 历史、对话映射、文件内容快照、回退事务、引用回收和可读摘要。

## 4. 术语

| 术语 | 含义 |
| --- | --- |
| Repository | 一个 Git 工作树根目录，或非 Git 项目的执行根目录 |
| Session | 一次 Codex 会话，对应一个 session/thread ID |
| Turn | 一次用户 prompt 及其后续 agent 工作 |
| Turn Item | 一个 turn 的可读索引和元数据记录 |
| Queue | 某个 session 的 turn item 滑动窗口 |
| State | 某个时刻的工作区文件 manifest |
| Object | CAS 中的文件内容 |
| Rewind Op | 一次可恢复的回退操作 |
| Retained Turn | 仍在 10 轮窗口中的终态 turn |
| Active Turn | 正在执行、尚未结束的 turn |

## 5. 总体架构

```text
Codex CLI
  │
  ├─ SessionStart ──────┐
  ├─ UserPromptSubmit ──┤
  ├─ PostToolUse ───────┼──> cxr hook ──> .codex-rewind/ 文件存储
  ├─ Stop ──────────────┤
  ├─ Interrupt ─────────┤
  └─ SessionEnd ────────┘

cxr CLI
  ├─ list/show
  ├─ rewind
  │    ├─ 读取 turn item 和 state
  │    ├─ 文件冲突预检
  │    ├─ 临时启动 codex app-server
  │    │    └─ thread/fork(beforeTurnId/lastTurnId)
  │    └─ 恢复工作区并输出 resume 命令
  ├─ clean
  └─ doctor
```

### 5.1 组件职责

| 组件 | 职责 |
| --- | --- |
| `cxr hook` | 处理 Codex hook 事件，维护 session、turn、queue 和 state |
| `cxr list/show` | 展示可读 turn 历史并支持选择 |
| `cxr rewind` | 创建对话分支、恢复文件、维护回退事务 |
| `cxr clean` | 清理过期 session、孤儿 state/object 和旧操作 |
| `cxr doctor` | 检查数据一致性、锁、版本兼容性和未完成操作 |
| Workspace Scanner | 使用 Git 边界枚举文件、读取内容和元数据 |
| Snapshot Store | CAS 内容存储和 state manifest 读写 |
| Codex Adapter | 启动临时 app-server，执行 thread 查询和 fork |
| Rewind Journal | 在文件系统中持久化可恢复的回退阶段 |

## 6. 文件系统布局

项目根目录下创建：

```text
<repo>/.codex-rewind/
  .gitignore
  config.json
  lock

  objects/
    ab/
      cd0123456789abcdef...

  states/
    <state-id>.json

  turn/
    <session-id>/
      session.json
      queue.json
      items/
        00000033-<turn-id>.json
      events/
        00000033-<turn-id>.jsonl

  rewind/
    <op-id>.json

  logs/
    hook-YYYY-MM-DD.jsonl

  tmp/
```

### 6.1 根目录解析

优先级：

1. hook 事件或 CLI 参数显式指定的 root；
2. `git rev-parse --show-toplevel`；
3. 当前目录向上查找最近的 `.codex-rewind/`；
4. 当前工作目录。

Git worktree 各自使用自己的 root 和 `.codex-rewind/`。

### 6.2 自忽略

`.codex-rewind/.gitignore` 内容为 `*`。同时扫描工作区时必须显式排除 `.git/`、`.codex-rewind/`、CAS 和 state 目录。不能只依赖 `.gitignore`。

### 6.3 风险提示

`.codex-rewind/` 位于项目根目录，以下操作仍可能删除数据：

- `git clean -fdx`
- `git clean -fdX`
- 用户手动删除目录
- 某些备份策略忽略未知隐藏目录

工具必须在 `doctor` 和首次安装时提示这一点。

## 7. 配置

`config.json` 示例：

```json
{
  "schemaVersion": 1,
  "retention": {
    "maxRetainedTurns": 10,
    "cleanAfterDays": 30
  },
  "limits": {
    "maxPromptBytes": 65536,
    "maxAssistantBytes": 65536,
    "maxEventBytesPerTurn": 1048576,
    "maxEventsPerTurn": 200,
    "maxDisplayFiles": 10,
    "maxDisplayTextChars": 500
  },
  "rewind": {
    "defaultMode": "fork",
    "allowInPlace": false
  },
  "hooks": {
    "recordPostToolUse": true
  }
}
```

配置解析失败时使用内置默认值，并在 `doctor` 中报告。

## 8. 数据模型

所有 JSON 文件必须包含 `schemaVersion`。所有写入使用临时文件、`fsync`、同目录 `rename`。

### 8.1 session.json

```json
{
  "schemaVersion": 1,
  "sessionId": "01a190c4-9444-7ff1-b4ac-4c4517af4b7d",
  "threadId": "01a190c4-9444-7ff1-b4ac-4c4517af4b7d",
  "source": "startup",
  "cwd": "/Users/apple/workspace/example",
  "gitRoot": "/Users/apple/workspace/example",
  "openedAt": "2026-10-31T15:00:00+08:00",
  "lastActivityAt": "2026-10-31T16:22:00+08:00",
  "closedAt": null,
  "title": "修复 rewind 存储",
  "open": true,
  "activeTurnId": null,
  "forkedFromSessionId": null,
  "forkedFromTurnId": null
}
```

`sessionId` 来自 hook，`threadId` 来自 app-server。正常情况下两者相同，但必须独立保存，避免未来 ID 语义变化。

### 8.2 queue.json

```json
{
  "schemaVersion": 1,
  "sessionId": "01a190c4-9444-7ff1-b4ac-4c4517af4b7d",
  "maxRetainedTurns": 10,
  "nextSeq": 43,
  "firstSeq": 33,
  "lastSeq": 42,
  "retainedTurnCount": 10,
  "turns": [
    {
      "turnId": "01a19106-5f26-7202-ab93-972e00207562",
      "seq": 42,
      "shortId": "01a19106",
      "status": "completed",
      "startedAt": "2026-10-31T16:20:00+08:00",
      "endedAt": "2026-10-31T16:22:00+08:00",
      "display": {
        "title": "修复 rewind 队列裁剪",
        "userText": "每个对话保留 10 轮，满了从头部删除……",
        "assistantText": "已加入 10 轮滑动窗口、可读 turn 摘要和 30 天清理。",
        "fileSummary": "3 files, +42 -8",
        "files": [
          "internal/rewind/queue.go",
          "internal/store/clean.go",
          "cmd/cxr/list.go"
        ]
      },
      "baselineStateId": "state-000033",
      "finalStateId": "state-000034",
      "itemPath": "items/00000042-01a19106-5f26-7202-ab93-972e00207562.json"
    }
  ]
}
```

规则：

- `turns` 按从旧到新排列；
- `seq` 在 session 内单调递增，永不重用；
- `nextSeq` 是下一个可用序号；
- `retainedTurnCount` 不包含 active turn；
- active turn 可以在 queue 中出现，但不参与 10 轮裁剪；
- `shortId` 只用于展示，不能作为唯一回退 ID；
- queue 损坏时从 `items/` 扫描重建。

### 8.3 Turn Item

```json
{
  "schemaVersion": 1,
  "turnId": "01a19106-5f26-7202-ab93-972e00207562",
  "seq": 42,
  "parentTurnId": "01a190f4-14ac-7cb1-bf9f-8004c98d2298",
  "status": "completed",
  "prompt": "每个对话保留 10 轮，满了从头部删除，并提供 clean 命令。",
  "lastAssistantMessage": "已加入 10 轮滑动窗口、可读 turn 摘要和 30 天清理。",
  "startedAt": "2026-10-31T16:20:00+08:00",
  "endedAt": "2026-10-31T16:22:00+08:00",
  "baselineStateId": "state-000033",
  "finalStateId": "state-000034",
  "changes": [
    {
      "path": "internal/rewind/queue.go",
      "status": "modified",
      "previousOid": "sha256:...",
      "currentOid": "sha256:...",
      "insertions": 42,
      "deletions": 8,
      "isBinary": false
    }
  ],
  "error": null,
  "needsFinalize": false
}
```

`status` 取值：`running`、`completed`、`interrupted`、`failed`。

### 8.4 State Manifest

第一版使用完整 manifest，便于恢复和诊断：

```json
{
  "schemaVersion": 1,
  "stateId": "state-000034",
  "parentStateId": "state-000033",
  "createdAt": "2026-10-31T16:22:00+08:00",
  "files": [
    {
      "path": "src/main.go",
      "exists": true,
      "oid": "sha256:...",
      "mode": 420,
      "isSymlink": false,
      "linkTarget": null,
      "isBinary": false,
      "size": 1234
    }
  ]
}
```

后续如果大仓库存储压力明显，可升级为增量 manifest + 周期完整 checkpoint，但不能改变 turn item 对 state 的引用语义。

### 8.5 CAS Object

对象路径为 `objects/<sha256 前两位>/<sha256 剩余部分>`，对象内容就是原始文件字节。

对象写入必须：

1. 计算 SHA-256；
2. 如果对象已存在且大小正确，直接复用；
3. 否则写入同目录临时文件；
4. `fsync` 后 rename；
5. 对象只读，目录权限 `0700`。

### 8.6 Event Log

每个 turn 一个有界事件日志：

```json
{"at":"2026-10-31T16:21:01+08:00","event":"PostToolUse","tool":"apply_patch","paths":["src/a.go"]}
{"at":"2026-10-31T16:21:03+08:00","event":"PostToolUse","tool":"Bash","paths":[]}
```

只记录元数据，不默认记录完整工具输出。日志达到 `maxEventsPerTurn` 或 `maxEventBytesPerTurn` 后滚动丢弃最旧事件。

### 8.7 Rewind Operation

```json
{
  "schemaVersion": 1,
  "opId": "rewind-01a19120-...",
  "sourceSessionId": "01a190c4-9444-7ff1-b4ac-4c4517af4b7d",
  "sourceThreadId": "01a190c4-9444-7ff1-b4ac-4c4517af4b7d",
  "targetTurnId": "01a19106-5f26-7202-ab93-972e00207562",
  "mode": "before",
  "phase": "planned",
  "safetyStateId": null,
  "targetStateId": "state-000033",
  "newThreadId": null,
  "conflicts": [],
  "createdAt": "2026-10-31T16:30:00+08:00",
  "updatedAt": "2026-10-31T16:30:00+08:00",
  "error": null
}
```

阶段定义：

```text
planned
  -> safety_captured
  -> thread_forked
  -> files_restoring
  -> verified
  -> completed

可恢复错误 -> failed_retryable
不可恢复错误 -> failed_terminal
```

## 9. Hook 设计

### 9.1 安装方式

`cxr install` 生成 hook 配置。支持：

```bash
cxr install --user
cxr install --repo
```

- `--user`：写入 `~/.codex/hooks.json`，所有项目生效；
- `--repo`：写入 `<repo>/.codex/hooks.json`，仅当前项目生效。

Hook 命令使用二进制绝对路径，例如：

```json
{
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "/Users/apple/bin/cxr hook", "timeout": 60}]}
    ],
    "UserPromptSubmit": [
      {"hooks": [{"type": "command", "command": "/Users/apple/bin/cxr hook", "timeout": 60}]}
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": "/Users/apple/bin/cxr hook", "timeout": 60}]}
    ],
    "Interrupt": [
      {"hooks": [{"type": "command", "command": "/Users/apple/bin/cxr hook", "timeout": 3}]}
    ],
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": "/Users/apple/bin/cxr hook", "timeout": 60}]}
    ]
  }
}
```

非 managed hook 需要用户在 Codex 中显式信任。安装器不得绕过信任流程。

### 9.2 通用输入

`cxr hook` 从 stdin 读取一个 JSON 对象，按 `hook_event_name` 分发。

处理开始前必须：

1. 读取并验证 JSON；
2. 确认 `hook_event_name` 受支持；
3. 解析 root；
4. 获取仓库级锁；
5. 加载或创建 session 目录；
6. 执行事件处理；
7. 原子落盘；
8. 释放锁。

### 9.3 事件处理矩阵

| Hook | 记录 | 内容快照 | 队列变化 |
| --- | --- | --- | --- |
| `SessionStart` | 创建/更新 session，记录来源 | 不主动全量扫描 | 不变 |
| `UserPromptSubmit` | 创建 turn item，记录 prompt | 采集 baseline state | 加入 active turn |
| `PreToolUse` | 可选事件记录 | 默认不做 | 不变 |
| `PostToolUse` | 追加紧凑事件 | 仅 dirty checkpoint | 更新 final pending |
| `Stop` | 记录 `last_assistant_message`、结束时间 | 采集 final state、计算 changes | active -> completed，触发裁剪 |
| `Interrupt` | 标记 interrupted、`needsFinalize=true` | 不做全量扫描 | active -> interrupted，触发裁剪 |
| `SessionEnd` | 更新 `lastActivityAt`、`closedAt` | 补齐未完成 final | 关闭 session |

### 9.4 状态快照与事件记录的区别

“每次 hook 都调用工具”不等于“每次 hook 都全量扫描整个仓库”。

- 每个 hook：写事件、更新 session/turn 元数据；
- turn 起点：采集 baseline；
- turn 终点：采集 final；
- 中途：只有检测到文件脏或配置开启时才写 checkpoint。

这样可以同时满足事件完整、回退所需状态完整，以及大仓库不会因每次工具调用都扫描全仓而不可用。

### 9.5 Hook 输出策略

- 普通 hook 不向 stdout 输出诊断信息；
- `Stop` 返回空输出或协议允许的 JSON；
- `Interrupt` 不输出长文本；
- 错误写 stderr 和 `.codex-rewind/logs/hook-YYYY-MM-DD.jsonl`；
- 默认 fail-open：快照失败不能阻止用户继续工作，但该 turn 标记为不可回退；
- 可选严格模式 `config.strictHook = true` 时阻止 prompt 启动。

### 9.6 Lazy Finalize

以下情况会留下未完成 turn：

- Codex 进程被强杀；
- `Interrupt` 后没有后续事件；
- `Stop` hook 失败；
- 机器断电。

任何后续 hook 或 `cxr rewind/doctor` 处理 session 时，都可以执行 lazy finalize：

1. 找到 `status=running` 或 `needsFinalize=true` 的 turn；
2. 采集当前工作区 state；
3. 标记 final state 和 changes；
4. 写入 `needsFinalize=false`；
5. 若无法证明 turn 已结束，则保持 `interrupted`，只保证 `--before` 可用。

## 10. 工作区扫描与快照

### 10.1 文件集合

默认文件集合：

```bash
git ls-files -z --cached --others --exclude-standard
```

并显式排除 `.git/`、`.codex-rewind/`、路径穿越和非法相对路径。

非 Git 目录使用有限深度的递归扫描，并读取最近的 `.gitignore` 作为可选过滤；第一版可以只支持 Git 仓库。

### 10.2 已有未跟踪文件

采集 baseline 时，已经存在的 untracked 文件必须记录真实内容，不能统一记为 `exists=false`。否则 rewind 会把用户原本就存在的文件误判为 agent 新增文件并删除。

### 10.3 增量采集

1. 读取上一次已知 state；
2. 使用 `git status --porcelain=v2 -z` 获取候选变化；
3. 对候选文件比较 `size`、`mtime`、`mode`；
4. 只有疑似变化的文件才读取内容并计算 SHA-256；
5. 未变化文件复用旧 OID；
6. 删除的文件写入 `exists=false`；
7. 新增文件写入真实 OID；
8. 写入新 state manifest。

### 10.4 文件属性

State 必须记录 `mode`、`size`、`isBinary`、`isSymlink` 和 `linkTarget`。

symlink 使用 `lstat` 读取，不跟随目标。恢复 symlink 时只重建链接本身，不写入链接目标。路径必须验证为仓库内相对路径。

### 10.5 一致性

文件可能在扫描期间被用户修改。实现应：

1. 读取 `lstat`；
2. 打开并读取内容；
3. 再次 `lstat`；
4. 如果 size/mtime 发生变化，重试一次；
5. 仍不稳定则记录为 `unstable`，该 turn 标记为回退可能不精确。

### 10.6 CAS 与对象校验

恢复前必须验证对象存在且哈希匹配。对象缺失或损坏时：

- 不执行部分恢复；
- 标记 rewind op 为 `failed_terminal`；
- 提示用户使用安全快照或 `cxr doctor`。

## 11. 10 轮队列

### 11.1 窗口定义

每个 session：

- 最多保留 10 个终态 turn；
- 终态包括 `completed`、`interrupted`、`failed`；
- active turn 不占用这 10 个名额；
- 第 11 个终态 turn 产生后，从队列头部淘汰最旧 turn。

### 11.2 新 turn 写入顺序

```text
1. 写 item 临时文件
2. 采集 baseline/final state 和 CAS 对象
3. 原子 rename item 文件
4. 更新 queue.json
5. 若终态 turn 超过 10 个，从头部移除
6. 删除被移除 turn 的 item 和 events 文件
7. 执行 state/object 可达性 GC
```

先写新数据，再更新 queue，最后删除旧数据。这样崩溃窗口最多产生孤儿文件，不会丢失 queue 仍引用的数据。

### 11.3 保留引用

删除 turn 时必须保留以下引用：

```text
所有 retained turn 的 baselineStateId
所有 retained turn 的 finalStateId
当前 active turn 的 baselineStateId
未完成 rewind op 的 targetStateId
未完成 rewind op 的 safetyStateId
```

不能直接把被淘汰 turn 对应的 state 和 object 按目录删除。被淘汰 turn 的 final state 可能是下一个 turn 的 baseline。

### 11.4 Queue 重建

queue 损坏时：

1. 扫描 `items/*.json`；
2. 按 `seq` 排序；
3. 只保留最近 10 个终态 turn；
4. 重建 `firstSeq/lastSeq/nextSeq`；
5. 原子重写 `queue.json`。

### 11.5 可读摘要

队列中的 `display` 是用户选择界面，不参与回退算法。

生成规则：

- `title`：prompt 第一行，去掉控制字符，最多 80 字符；
- `userText`：prompt 去控制字符后最多 500 字符；
- `assistantText`：`last_assistant_message` 去控制字符后最多 500 字符；
- `fileSummary`：`N files, +X -Y`；
- `files`：最多 10 个变化路径；
- 超长文本按 UTF-8 字符边界截断，不能截断出非法编码。

不额外调用模型生成摘要。人类可读内容来自 hook 输入和本地 diff 统计。

## 12. Rewind 设计

### 12.1 命令

```bash
cxr list [--session <id>] [--all] [--json]
cxr show --session <id> --seq <n>
cxr rewind --last
cxr rewind --session <id> --seq <n> --before
cxr rewind --session <id> --turn <turn-id> --through
cxr rewind --session <id> --seq <n> --before --dry-run
cxr rewind --session <id> --seq <n> --before --resume
```

选择规则：

- `--seq` 只在指定 session 内有效；
- `--turn` 接受完整 turn UUID；
- 未指定 session 时，默认最近活跃 session；
- 目标 turn 已不在 10 轮窗口时直接报错；
- 不自动选择更近的 turn 替代目标。

### 12.2 before/through 语义

| 模式 | 对话 | 文件 |
| --- | --- | --- |
| `--before` | `thread/fork.beforeTurnId = T` | 恢复 T 的 `baselineStateId` |
| `--through` | `thread/fork.lastTurnId = T` | 恢复 T 的 `finalStateId` |

如果 T 是 `interrupted` 且没有 final state：

- `--before` 可用；
- `--through` 返回明确错误。

### 12.3 默认软 fork

默认行为：

1. 保留原 session 和原 thread；
2. 创建新 thread；
3. 用 `thread/name/set` 给新 thread 设置可辨识名称，使其立即出现在 resume 列表；
4. 恢复工作区到目标 state；
5. 输出 `codex resume <new-thread-id>`。

原 thread 不删除、不重写、不裁剪。命名失败只产生 warning，不回滚 fork，也不阻止文件恢复。

### 12.4 In-place 模式

`thread/revert` 仅作为未来选项，第一版不实现自动 in-place。若实现，必须显式 `--in-place`、显示将删除的 turn、要求二次确认、先写 rewind op，并保留 safety state 直到操作完成。

### 12.5 活动会话检查

执行文件恢复前必须确认：

- 目标 session 没有 active turn；
- 仓库没有其他 session 的 active turn；
- 没有持锁的 hook；
- 没有未完成的 rewind op。

如果无法确认活动状态，默认拒绝执行。`--force` 只能用于显式覆盖 stale lease，不能静默绕过正在运行的 Codex。

### 12.6 Rewind 事务顺序

```text
1. 读取 queue 和 target state
2. 生成文件恢复计划
3. 检测所有 conflict
4. 拒绝可回滚且存在 conflict 的默认操作
5. 采集当前工作区 safety state
6. 写 rewind op: planned/safety_captured
7. 临时启动 app-server
8. thread/fork
9. thread/name/set（best-effort；失败只记录 warning）
10. 写 rewind op: thread_forked，保存 newThreadId/newThreadName
11. 按计划恢复文件
12. 重新扫描并验证目标 manifest
13. 写 rewind op: verified/completed
14. 输出 resume 命令
```

如果第 8 步或第 10 步失败：

- 不恢复文件；
- 保留原 thread；
- op 标记为可重试。

如果第 11–12 步失败：

- 不输出 resume 命令；
- 使用 safety state 尝试恢复；
- op 标记 failed_retryable 或 failed_terminal；
- 保留 newThreadId 供诊断，但不得重复 fork。

### 12.7 文件恢复算法

恢复范围只包括被丢弃 turn 拥有过的路径：

```text
discardedTurns = target 之后的所有 retained/active turns
paths = union(discardedTurns.changes.path)
```

对每个路径：

1. 查找该路径在最近记录 state 中的预期 OID；
2. 计算当前文件 OID；
3. 不一致则记为 conflict；
4. 默认发现 conflict 就停止；
5. `--force` 才覆盖；
6. 目标 state 不存在且当前文件属于 discarded turn 新增时，才删除；
7. 不属于 discarded turn 的未跟踪文件绝不删除；
8. 恢复文件使用临时文件 + rename；
9. symlink 和 mode 单独恢复；
10. 恢复后重新扫描并校验目标 manifest。

### 12.8 不恢复的内容

第一版明确不恢复：

- Git index/staging area；
- `.git/` 内容；
- ignored 文件；
- 数据库写入；
- 网络请求；
- 容器、进程、后台服务；
- 用户在主仓库外创建的文件。

命令输出必须说明这些边界，不能让用户误以为整个系统状态都被回退。

## 13. Clean 与 GC

### 13.1 清理策略

`clean` 和 10 轮窗口是两套独立策略：

- 10 轮窗口控制每个 session 的回退深度；
- `clean` 清理超过 30 天没有活动的 session、孤儿 state/object 和旧操作。

默认命令：

```bash
cxr clean --older-than 30d --dry-run
cxr clean --older-than 30d --yes
cxr clean --orphans --older-than 30d --yes
```

删除必须显式 `--yes`；默认只展示计划。

### 13.2 Session 过期

以 `session.json.lastActivityAt` 为准，不以单个 turn 的时间为准。

删除 session 的条件：

- `lastActivityAt < now - 30d`；
- `activeTurnId == null`；
- 默认只删除 `closedAt != null` 的 session；
- 未正常关闭的 session 需要 `--include-stale-open` 显式处理。

只要 session 仍在最近活跃范围内，它的 10 轮窗口就是回退边界，不因某个 turn 较旧而单独删除。

### 13.3 孤儿数据

clean 的执行顺序：

```text
1. 获取仓库锁
2. 删除过期 session 目录
3. 删除已完成的旧 rewind op
4. 扫描所有 retained state
5. 删除不可达 state
6. 删除不被 state 引用的 object
7. 清理 tmp 和损坏部分文件
8. 写 clean 日志
```

严禁按 `objects/` 文件的 mtime 直接删除对象。一个对象可能很旧，但仍被最近 session 的 state 引用。

### 13.4 未完成 rewind op

未完成 op 默认不自动删除。`doctor` 应报告 op 阶段、是否已经 fork、newThreadId、safety state，以及可继续、需重试或需人工确认。

只有显式 `clean --include-incomplete --yes` 才允许删除。

### 13.5 引用算法

```text
retainedStates =
  sessions not expired 的所有 retained turn 的 baseline/final
  + active turn 的 baseline
  + 所有未完成 rewind op 的 safety/target state

1. 删除不在 retainedStates 中的 state 文件
2. 收集 retainedStates 中所有 oid
3. 删除不在该集合中的 object
```

GC 可以在每次队列裁剪后增量执行，也可以在 `clean` 时全量执行。第一版建议裁剪后只做轻量清理，完整 GC 放在 `clean`。

## 14. CLI 交互

### 14.1 列表

```text
SESSION 01a190c4  修复 rewind 存储

[40] 01a190d6  16:10  completed
     You: 队列满了怎么处理旧 turn？
     AI : 从头部删除，并做 state 可达性检查。
     2 files +18 -4

[41] 01a190f4  16:15  completed
     You: 增加人类可读的轮次摘要。
     AI : 已增加 prompt、assistantText 和文件名预览。
     1 file +27 -2

[42] 01a19106  16:20  completed
     You: 每个对话保留 10 轮，并提供 clean。
     AI : 已加入 10 轮窗口和 30 天清理。
     3 files +42 -8
```

### 14.2 其他命令

```bash
cxr status
cxr doctor
cxr gc --dry-run
cxr uninstall --user
cxr uninstall --repo
```

`doctor` 应检查 Codex CLI 版本、app-server 是否支持 `thread/fork.beforeTurnId`、hook 是否已安装并受信任、queue 与 item 是否一致、state/object 是否缺失、是否有未完成 rewind op，以及是否有 stale session 锁。

## 15. 安全与隐私

1. `.codex-rewind/` 和 CAS 目录权限为 `0700`。
2. prompt 和 assistant 摘要只保存在本地，不发送到网络。
3. 默认只保存有长度上限的 prompt/assistant 片段，不复制完整 transcript。
4. 不解析 `transcript_path`。
5. hook 命令使用绝对路径，不拼接用户输入到 shell。
6. session 目录名只接受合法 UUID 或严格白名单字符。
7. 所有相对路径必须解析后仍在 workspace root 内。
8. symlink 不跟随，防止通过链接访问仓库外文件。
9. 写入前校验目标路径和父目录不存在路径穿越。
10. `clean` 和 `rewind` 必须尊重文件锁和未完成操作。

## 16. Go 项目结构

建议 module 名：`github.com/<owner>/codex-rewind`。

```text
cmd/
  cxr/
    main.go

internal/
  cli/
    root.go
    hook.go
    list.go
    rewind.go
    clean.go
    doctor.go
    install.go

  app/
    hookservice/
    rewindservice/
    cleanservice/
    doctorservice/

  codex/
    appserver/
      client.go
      protocol.go
      capability.go

  workspace/
    root.go
    git.go
    scan.go
    hash.go

  snapshot/
    cas.go
    state.go
    restore.go
    conflict.go

  store/
    layout.go
    session.go
    queue.go
    turn.go
    rewindop.go
    atomic.go
    lock.go

  display/
    summary.go
    text.go

  journal/
    phases.go

  gc/
    reachability.go
```

### 16.1 依赖策略

- JSON、SHA-256、文件操作、进程调用使用 Go 标准库；
- CLI 解析可以使用标准库 `flag`，若子命令继续增长再评估 `cobra`；
- 跨平台文件锁可以使用一个小的 `flock` 包装；Linux/macOS 第一版可用 `flock(2)`，Windows 后续补 `LockFileEx`；
- 不引入 SQLite；
- 不引入 PTY 库；
- 不引入终端 UI 框架，列表和选择使用普通 stdout/stdin。

### 16.2 二进制策略

第一版使用一个 `cxr` 二进制，通过隐藏子命令处理 hook：

```bash
cxr hook
```

如果后续发现 hook 启动时间成为问题，再拆出极简 `cxr-hook`，但不改变存储协议。

## 17. 实施里程碑

### M0：协议探测与只读骨架

交付：

- `cxr doctor`；
- hook JSON 解析；
- session/turn 目录创建；
- `cxr list`；
- 临时 app-server 客户端；
- 验证 hook `turn_id` 与 app-server turn ID 是否一致；
- 验证 `thread/fork(beforeTurnId)`。

验收：

- 不改任何工作区文件；
- 每个 hook 进程执行完立即退出；
- 无 daemon 残留；
- 能列出人类可读 turn。

### M1：快照与 10 轮队列

交付：

- Git 文件枚举；
- baseline/final state；
- CAS；
- turn item；
- queue 裁剪；
- 可读摘要；
- queue 重建。

验收：

- 连续 11 个 turn 后只保留 10 个；
- 最旧 turn 被删除；
- 被保留 turn 的 state 不被误删；
- 未跟踪文件 baseline 正确；
- 用户手动创建的文件不会被误判成 agent 新文件。

### M2：软 fork rewind

交付：

- `cxr rewind --before/--through/--last`；
- app-server fork；
- 文件恢复；
- conflict 检测；
- rewind journal；
- `codex resume <id>` 输出。

验收：

- 原 thread 不变；
- 新 thread 的历史截止正确；
- 工作区精确恢复；
- 手动修改产生 conflict；
- 重新执行不会重复 fork。

### M3：Clean 与 GC

交付：

- `cxr clean --older-than 30d`；
- dry-run；
- 过期 session；
- 孤儿 state/object；
- 引用扫描；
- 旧 rewind op 清理。

验收：

- 共享 object 不会被误删；
- 30 天内 session 不被清理；
- 过期 session 的独占对象被回收；
- 未完成 rewind op 不被静默删除。

### M4：健壮性与发布

交付：

- 崩溃恢复；
- 并发 session；
- stale lock 检测；
- 版本兼容检查；
- 安装/卸载 hook；
- 文档和发布脚本。

## 18. 测试计划

### 18.1 单元测试

- 相对路径和路径穿越校验；
- symlink 不跟随；
- SHA-256 对象写入和复用；
- baseline 记录已存在 untracked 文件；
- 10 轮队列裁剪；
- queue 损坏重建；
- 可读文本截断；
- 引用扫描；
- conflict 检测；
- rewind phase 状态机。

### 18.2 集成测试

使用临时 Git 仓库和临时 `CODEX_HOME`：

1. 构造 11 个 turn；
2. 验证队列只剩 10 个；
3. 回退到最旧 retained turn；
4. 验证对话 fork 的 turn 数；
5. 验证工作区文件内容；
6. 人为制造 queue 损坏并重建；
7. 人为制造 object 缺失并验证 fail closed；
8. 模拟 interrupted turn；
9. 模拟 clean 前后共享对象引用。

### 18.3 端到端测试

建议使用 `codex exec --json` 或直接向 `cxr hook` 注入官方 hook JSON 来构造确定性测试。app-server 测试必须：

- 每次启动新进程；
- 声明 `experimentalApi`；
- 请求完成后关闭 stdin；
- 超时后杀进程；
- 验证无残留进程。

### 18.4 验收清单

- [ ] 项目根目录只新增 `.codex-rewind/`，不污染 `.git/`；
- [ ] `.codex-rewind/` 不会出现在 `git status`；
- [ ] 每个 session 最多 10 个可回退终态 turn；
- [ ] active turn 不占 10 个名额；
- [ ] `list` 能显示 user/assistant 摘要和文件名；
- [ ] `--before` 恢复 baseline；
- [ ] `--through` 恢复 final；
- [ ] 原 thread 在软 fork 后保持完整；
- [ ] 手动修改会触发 conflict；
- [ ] 第 11 个 turn 后最旧 item 消失；
- [ ] 旧 turn 的 state 在仍被引用时不会被删；
- [ ] `clean` 默认 dry-run，必须显式确认才删除；
- [ ] `clean` 按 session 最近活动时间清理；
- [ ] 未完成 rewind op 不会被静默删除；
- [ ] 所有 hook 进程执行完立即退出；
- [ ] 没有 PTY、没有常驻 daemon、没有 Codex 源码修改。

## 19. 风险与缓解

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| app-server fork 属于实验协议 | 升级后不可用 | 版本探测、能力探测、fail closed、M0 锁定 |
| hook 未受信任 | 不产生数据 | `doctor` 检查并提示用户信任 |
| hook 超时或崩溃 | turn 不可回退 | lazy finalize、fail-open、日志 |
| 大仓库扫描慢 | prompt 延迟 | 增量扫描、stat 预筛选、事件与内容分离 |
| 用户手动改文件 | 回退覆盖手工成果 | conflict 检测、默认拒绝 |
| 多会话并发 | queue/state 冲突 | 仓库锁、session 锁、active turn 检查 |
| ignored 文件不在快照 | 部分文件无法恢复 | 文档明确边界，不默认支持 ignored |
| Git index 不恢复 | staged 状态不一致 | 第一版明确边界，后续单独设计 |
| `.codex-rewind/` 被 clean 删除 | 历史丢失 | 自忽略、doctor 警告、文档提示 |
| `transcript_path` 格式变化 | 解析失败 | 不解析 transcript |

## 20. 待决问题

1. 是否允许 `clean` 清理未正常关闭但超过 30 天的 session？当前建议默认不允许。
2. 是否需要在第一版支持非 Git 目录？当前建议只支持 Git 仓库。
3. 是否恢复 Git index？当前建议第一版不恢复，仅报告 staged 状态。
4. 是否提供交互式选择器？当前建议先提供表格和 `--seq`，后续再加。
5. 是否提供 `--in-place`？当前建议第一版不实现，避免不可逆删除历史。
6. 是否需要把 `events.jsonl` 作为调试信息默认开启？当前建议有界默认开启，允许配置关闭。

## 21. 附录：典型命令流

### 21.1 安装

```bash
cxr install --user
codex  # 按提示信任 hook
cxr doctor
```

### 21.2 查看与选择

```bash
cxr list
cxr show --session 01a190c4-9444-7ff1-b4ac-4c4517af4b7d --seq 42
```

### 21.3 回退到某个 turn 之前

```bash
cxr rewind --session 01a190c4-9444-7ff1-b4ac-4c4517af4b7d --seq 42 --before --dry-run
cxr rewind --session 01a190c4-9444-7ff1-b4ac-4c4517af4b7d --seq 42 --before
# 根据输出执行
codex resume <new-thread-id>
```

### 21.4 撤销最后一轮

```bash
cxr rewind --last
```

### 21.5 清理

```bash
cxr clean --older-than 30d --dry-run
cxr clean --older-than 30d --yes
```

## 22. 最终设计结论

该方案的关键不是“把文件复制一份”，而是建立三个稳定边界：

1. Git 决定文件集合，不承担历史数据库职责；
2. `.codex-rewind/` 用有限队列、独立 item 和 CAS 承担本地状态；
3. app-server 只承担对话 fork，不常驻、不控制 TUI。

第一版应当优先保证：

- 10 轮窗口语义清晰；
- 每轮有可读选择依据；
- 回退前有冲突检查；
- 删除旧 turn 不破坏 retained state；
- clean 先做可达性扫描，再删除对象；
- 所有失败都可诊断、可重试、可人工恢复。
