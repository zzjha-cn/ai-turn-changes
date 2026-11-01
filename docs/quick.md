需要。`cxr` 本身不用改 Codex 源码，但必须把它的生命周期 hook 写进 Codex 的 `hooks.json`，否则不会记录任何 turn。安装器会替你写配置，你只需要在 Codex 里 **信任一次**。

## 最短启动流程

### 1. 安装二进制

```bash
cd /Users/apple/workspace/mg/ag/art
go build -o ~/.local/bin/cxr ./cmd/cxr
cxr version
```

你机器上的 `~/.local/bin` 已经在 `PATH` 里。

### 2. 安装 hook（二选一，不要同时装）

**推荐：所有项目生效**

```bash
cxr install --user
```

写入 `~/.codex/hooks.json`。

**或者：只在当前仓库生效**

```bash
cd /path/to/your/repo
cxr install --repo
```

写入 `<repo>/.codex/hooks.json`。这个文件会出现在 `git status` 中，是否提交由你决定；运行数据仍然只写在自忽略的 `.codex-rewind/` 里。

安装器会注册 6 个事件：

```text
SessionStart
UserPromptSubmit
PostToolUse
Stop
Interrupt
SessionEnd
```

不需要你手动编辑 JSON，也不需要配置 `cxr hook` 的参数。

### 3. 在 Codex 里信任 hook

启动一个新的 Codex 会话：

```bash
cd /path/to/your/repo
codex
```

然后在 Codex 输入：

```text
/hooks
```

找到刚安装的 `cxr hook` 条目，逐项批准/信任。Codex 对非 managed hook 不会自动信任；**不信任就不会执行，也不会产生数据**。

注意：已经开着的 Codex 会话不会加载新 hook，必须新开一个会话。

### 4. 正常使用 Codex，产生几个 turn

```bash
codex
```

正常对话几轮，然后退出。之后检查：

```bash
cxr status
cxr list
```

第一次 hook 执行后才会出现 `.codex-rewind/`，这是正常的。

### 5. 回退

先看计划，不修改任何东西：

```bash
cxr rewind --last --dry-run
```

或：

```bash
cxr list
cxr rewind --session <session-id> --seq 3 --before --dry-run
```

确认后执行：

```bash
cxr rewind --session <session-id> --seq 3 --before
```

然后按输出继续：

```bash
codex resume <new-thread-id>
```

原 thread 不会被删除或改写，这是软 fork。

## 安装后自检

在仓库目录里执行：

```bash
cxr doctor
```

应看到：

```text
[ok] codex: version 0.164.0
[ok] capability: thread/fork.beforeTurnId available
[ok] hooks: installed in .../hooks.json
```

`layout: no .codex-rewind/` 在还没跑过 hook 时是预期的，跑完第一轮后会消失。

## 常见问题

- **`cxr list` 一直为空**：hook 没有受信任。回到 Codex 执行 `/hooks` 批准；或确认安装的是 `--user` 还是 `--repo`，以及启动 Codex 的环境是否使用同一个 `CODEX_HOME`。
- **移动了 `cxr` 二进制**：`hooks.json` 里写的是安装时的绝对路径，移动后需要重新执行一次 `cxr install`。
- **自定义 `CODEX_HOME`**：`cxr install --user` 和 Codex 必须使用同一个 `CODEX_HOME`。
- **不要同时执行 `cxr install --user` 和 `cxr install --repo`**：两个作用域可能都触发 hook，造成重复记录。
- **只支持 Git 仓库**：在非 Git 目录中不会正常采集。
- **ignored 文件不记录**：`.gitignore` 忽略的文件不在快照范围内。

按上面 1–4 步走完，就可以实际开始用了。

----

它们分别在以下时机运行：
事件	作用
SessionStart	创建 session 记录
UserPromptSubmit	开始新 turn，采集 baseline
PostToolUse	记录工具调用触达的路径
Stop	结束 turn，采集 final state
Interrupt	处理中断的 turn
SessionEnd	关闭 session，触发队列裁剪
代码层面的安全边界：
