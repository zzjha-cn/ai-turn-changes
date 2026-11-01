# 基于 Turn 的工作区状态锁存与 Undo（回滚）技术实现指南

## 1. 适用场景

本指南定义了一种通用的**“工作区断面锁存与物理恢复协议”**。当你的应用（如测试 Harness、LLM 自动重构引擎、CLI 执行沙箱）需要支持以下功能时，可以完整移植本方案：

1.  **Undo 撤销指令**：在 AI 批量修改文件或 Harness 破坏性运行后，一键无缝将工作区恢复到执行前。
2.  **事务沙箱**：运行测试前锁存状态，运行结束后无论成功与否，物理还原工作区，防止脏数据污染。
3.  **多分支历史回溯**：在交互式会话中，支持回到任意一个历史轮次（Turn）的断层。

---

## 2. 核心架构与四大组件

要实现一个完备且不污染用户仓库的 Undo 系统，你需要构建四个核心解耦组件：

```
+-----------------------------------------------------------------------+
|                            Harness Pipeline                           |
+-----------------------------------------------------------------------+
        |                                                       ^
   1. saveState()                                          4. undo()
        v                                                       |
+-----------------+      2. store()       +-----------------------------+
|  State Capturer | --------------------> | Content-Addressable Storage |
+-----------------+                       |       (Snapshot Store)      |
        |                                 +-----------------------------+
        | 3. compare()                                   ^
        v                                                | read()
+-----------------+                                      |
|   Diff Engine   | =====================================+
+-----------------+
```

### 2.1 State Capturer (状态采集器)
*   **职责**：枚举工作区内所有合法文件（通常结合 Git/Ignore 规则过滤），扫描每个文件的相对路径、长度和哈希值（哈希作为该内容的唯一标识 `oid`），构建当前的 `WorkspaceState`。
*   **不存本体**：采集器只生成文件的结构化索引（Manifest），不负责存储文件内容本体。

### 2.2 Content-Addressable Storage (CAS, 内容寻址对象存储)
*   **职责**：以内容哈希（SHA-1 或 SHA-256）作为物理文件名，将文件内容本体写入应用私有的存储目录中（不写入用户仓库）。
*   **天然去重**：相同内容的文件无论在工作区出现多少次，在 CAS 中仅存一份。

### 2.3 Diff Engine (差异引擎)
*   **职责**：比较两个 `WorkspaceState` 断面（State A 与 State B），计算出增量（Delta）变化，生成包含 `added`、`modified`、`deleted` 状态的文件变更集合。

### 2.4 Undo Engine (回滚引擎)
*   **职责**：接受一个目标 `WorkspaceState`，对比当前工作区状态，执行物理覆盖、物理还原和物理删除，使工作区百分之百还原至目标断层。

---

## 3. 核心数据模型协议 (JSON Schema)

```typescript
// 1. 单个文件元数据
interface FileMeta {
  path: string;       // 工作区相对路径，统一用 '/' 作为分隔符
  oid: string;        // 内容哈希值，作为唯一 OID (Object ID)
  isBinary: boolean;  // 是否是二进制文件
}

// 2. 物理断层状态
interface WorkspaceState {
  stateId: string;    // 状态唯一标识（通常是时间戳或随机 UUID）
  createdAt: number;  // 采集时间戳
  files: FileMeta[];  // 工作区内所有受控文件的 Manifest 列表
}

// 3. 事务记录 (Turn / Transaction)
interface TurnRecord {
  turnId: number;     // 轮次单调递增 ID
  parentId?: number;  // 父轮次 ID
  baseline: WorkspaceState;    // 开始本轮前的基线状态
  finalState: WorkspaceState;  // 结束本轮后的物理状态
  changes: FileChange[];       // 这一轮具体改了什么 (Delta)
}
```

---

## 4. Undo (物理回滚) 核心算法与步骤

当 Harness 或用户发起 `undo(targetState: WorkspaceState)` 时，回滚引擎必须严格执行以下三步物理操作，以保证工作区绝对干净地还原。

```
+-----------------------------------------------------------------+
|                       Target State Manifest                     |
+-----------------------------------------------------------------+
          |                             |
    (File exists in target)       (File missing in target)
          |                             |
          v                             v
+-----------------------+     +-------------------+
| Is Workspace version  |     |   Delete file in  |
| different/missing?    |     |     Workspace     |
+-----------------------+     +-------------------+
     /             \
   Yes             No
   /                 \
+----------------+  [Keep as is]
| Restore from   |
| CAS to Workdir |
+----------------+
```

### 步骤 A：扫描当前工作区实态
首先，实时扫描当前物理工作区，生成 `currentState: WorkspaceState`。

### 步骤 B：执行“还原”与“物理覆盖”（Restore & Overwrite）
遍历 `targetState.files` 中的每一个文件：
1.  如果在 `currentState` 中**不存在**该文件（表示该文件被 AI/Harness 删除了）：
    *   **Action**：从 CAS 存储中读取 `targetFile.oid` 对应的内容，在工作区重建该文件。
2.  如果在 `currentState` 中**存在**该文件，但 `currentStateFile.oid !== targetFile.oid`（表示文件被修改了）：
    *   **Action**：从 CAS 存储中读取 `targetFile.oid` 对应的内容，**强行物理覆盖**工作区对应的文件。
3.  如果哈希一致，则不做任何处理。

### 步骤 C：执行“清理”与“物理删除”（Clean & Unlink）
遍历 `currentState.files` 中的每一个文件：
1.  如果在 `targetState.files` 中**不存在**该文件（表示该文件是这一轮新创建的临时文件或测试产物）：
    *   **Action**：在工作区中将该文件**直接物理删除 (unlink)**，并安全清理空的父目录。

---

## 5. 垃圾回收与防悬空设计 (Garbage Collection)

如果你的 Harness 会频繁生成状态并执行 Undo，CAS 目录会迅速堆积废弃的文件快照。系统必须实现基于**引用计数**的回收机制：

### 5.1 引用关系链
*   每一个物理快照 `oid` 的生命周期，由**引用计数 (Ref Count)** 控制。
*   只要该快照存在于任何一个 `TurnRecord` 的 `baseline` 或 `finalState` 中，其引用计数 >= 1。
*   当前正在运行但尚未结束的 `ActiveCandidate` 也会临时锁定它所持有的 OID 引用。

### 5.2 物理回收触发
1.  **删除轮次/回滚裁剪**：当你决定抛弃某个历史轮次，或在 Undo 到第 N 轮后决定剪枝（Prune）掉 N 之后的废弃分支时，释放这些废弃 `WorkspaceState` 里所有 `FileMeta.oid` 的引用：
    $$\text{RefCount}(oid) = \text{RefCount}(oid) - 1$$
2.  **物理擦除**：当某个 `oid` 的 `RefCount` 归零时，`SnapshotStore` 必须**立刻执行磁盘物理删除**：
    ```typescript
    if (refCount[oid] === 0) {
      fs.unlinkSync(path.join(storageDir, oid));
      delete refCount[oid];
    }
    ```
这保证了在整个 Undo 历史树的演进中，**绝不会发生快照提前被删导致的 `Snapshot not found`**，同时也绝不会在本地漏掉任何一个孤儿（Orphan）快照文件。

---

## 6. 通用 TypeScript 接口定义 (可直接移植)

你可以直接将以下接口和骨架代码拷贝到其他应用的底层存储模块中：

```typescript
import * as fs from 'fs';
import * as path from 'path';

export interface FileMeta {
  path: string;
  oid: string;
  isBinary: boolean;
}

export interface WorkspaceState {
  stateId: string;
  createdAt: number;
  files: FileMeta[];
}

export class WorkspaceUndoHarness {
  constructor(
    private workspaceRoot: string,
    private storageDir: string // 扩展私有存储路径
  ) {}

  /**
   * 1. 采集当前工作区断面并写入 CAS 存储
   */
  public async captureState(): Promise<WorkspaceState> {
    const files = await this.scanWorkspaceFiles();
    const fileMetas: FileMeta[] = [];

    for (const file of files) {
      const content = fs.readFileSync(file);
      const oid = this.calculateHash(content);

      // 写入 CAS 寻址存储
      const objectPath = path.join(this.storageDir, oid);
      if (!fs.existsSync(objectPath)) {
        fs.writeFileSync(objectPath, content);
      }

      fileMetas.push({
        path: path.relative(this.workspaceRoot, file),
        oid,
        isBinary: this.checkIsBinary(content)
      });
    }

    return {
      stateId: `state_${Date.now()}`,
      createdAt: Date.now(),
      files: fileMetas
    };
  }

  /**
   * 2. 执行 Undo (回滚) 到目标状态
   */
  public async undo(target: WorkspaceState): Promise<void> {
    const current = await this.captureState();
    const currentMap = new Map(current.files.map(f => [f.path, f]));
    const targetMap = new Map(target.files.map(f => [f.path, f]));

    // Step A: 还原和覆盖 (Restore & Overwrite)
    for (const targetFile of target.files) {
      const currentFile = currentMap.get(targetFile.path);
      const absPath = path.join(this.workspaceRoot, targetFile.path);

      if (!currentFile || currentFile.oid !== targetFile.oid) {
        // 文件缺失或被改动，从存储恢复
        const content = fs.readFileSync(path.join(this.storageDir, targetFile.oid));
        fs.mkdirSync(path.dirname(absPath), { recursive: true });
        fs.writeFileSync(absPath, content);
      }
    }

    // Step B: 清理新增的脏文件 (Clean untracked files)
    for (const currentFile of current.files) {
      if (!targetMap.has(currentFile.path)) {
        const absPath = path.join(this.workspaceRoot, currentFile.path);
        if (fs.existsSync(absPath)) {
          fs.unlinkSync(absPath);
          this.cleanEmptyDirs(path.dirname(absPath));
        }
      }
    }
  }

  private calculateHash(buf: Buffer): string {
    return require('crypto').createHash('sha256').update(buf).digest('hex');
  }

  private async scanWorkspaceFiles(): Promise<string[]> {
    // 基于你的 Harness 的文件枚举逻辑 (遵循 Ignore 规则)
    return [];
  }

  private checkIsBinary(buf: Buffer): boolean {
    return false; // 简易判定
  }

  private cleanEmptyDirs(dir: string) {
    if (dir === this.workspaceRoot) return;
    try {
      if (fs.readdirSync(dir).length === 0) {
        fs.rmdirSync(dir);
        this.cleanEmptyDirs(path.dirname(dir));
      }
    } catch {}
  }
}
```

---

## 7. 真实生产环境参考实现

如果你在后续重构或设计新系统时，需要看本系统是如何具体落地这些组件的，可以直接查阅本项目以下文件中的模块：

1.  **快照管理与物理寻址实现**：`SnapshotStore.ts` —— 包含完整的哈希命名空间、文件追加引用、物理 `unlink` 与垃圾回收逻辑。
2.  **快照引用锁定与主事务生命周期**：`TurnManager.ts` —— 展示了如何优雅地利用 `try {} finally {}` 机制在录制、结算和异常丢弃时强行释放和回收快照。
3.  **两态比对计算**：`DiffEngine.ts` —— 说明了如何实现两态之间的 `compareStatesSync`，并生成完整的 Delta。

---

## 8. 局限性与应对措施 (Engineering Trade-offs)

虽然这套基于 Turn 的“内容寻址快照 + 物理回滚”方案在隔离性、Git 无污染和多并发沙箱上表现极佳，但在生产环境下，你必须注意并处理以下工程缺陷。

### 8.1 缺点 1：无差别的全量文件存储 (Storage Overhead)
*   **局限性**：CAS（内容寻址存储）使用的是**文件物理全量存储**。即使一个 10MB 的文件你只修改了 1 行代码，由于计算出的哈希变了，系统也会在本地私有目录中重新写入一个全新的 10MB 物理文件。
*   **后果**：在频繁触发 Undo 事务或存在超大文件（如大打包产物、资源大图、本地嵌入式 SQLite DB）的工作区中，存储开销会迅速膨胀。
*   **应对措施**：
    1.  **主动排除（Strict Ignore）**：在文件扫描器中引入严格白名单或排除黑名单，禁止扫描任何非源文件。
    2.  **块级增量压缩（Chunking / Delta-Patch）**：在未来的系统演进中，可引入滑动窗口切片（如 Rolling Hash）只存储修改的数据块，或在 CAS 目录中只存储 Hunk-Diff Patch，在 `undo` 执行时动态逆向合并恢复。

### 8.2 缺点 2：大仓库下的 I/O 与哈希计算瓶颈 (I/O Bottleneck)
*   **局限性**：在 `captureState()` 阶段，系统需要对工作区文件进行遍历、物理读取和哈希值计算。
*   **后果**：如果仓库规模极其庞大（例如上万个源文件，且 `node_modules` 未被正确配置在排除列表里），高频的状态采集会产生毫秒级甚至秒级的卡顿，阻塞 Harness 运行流。
*   **应对措施**：
    1.  **文件监听增量缓存（Watcher-based Cache）**：不采用“全量遍历扫描”，而是挂载一个背景 File Watcher。只有发生变化的文件才会在缓存中更新 OID，`captureState` 时直接读取内存 Manifest。
    2.  **节流与自适应轮询**：如本项目所实现的，对输入触发事件进行 500ms 缓存节流聚合（Coalesce），空闲期自动降频轮询，避免对磁盘执行无效的高频扫描。

### 8.3 缺点 3：物理系统属性与软链接丢失 (Metadata / Symlink Loss)
*   **局限性**：为了跨平台去重，CAS 只关注和锁存**纯文本/二进制内容本身**。
*   **后果**：
    *   **可执行属性丢失**：Unix 系统下的 `chmod +x` 可执行脚本在物理回滚后，会退化为普通文本，导致后续指令因权限不足（Permission Denied）报错。
    *   **软链接失效**：软链接文件（Symbolic Links）在采集时会被误读为它所指向的路径文本，回滚后直接变为一个普通文本文件，丢失软链特性。
*   **应对措施**：
    1.  **扩展元数据元组（Enriched Metadata）**：在 `FileMeta` 数据结构中不仅保存 `path` 和 `oid`，还额外采集其系统属性：
        ```typescript
        interface FileMeta {
          path: string;
          oid: string;
          isBinary: boolean;
          mode?: number;       // 文件系统权限 mode (如 0o755)
          symlinkTarget?: string; // 如果是软链接，记录链接指向的相对路径
        }
        ```
    2.  **系统级自愈调用**：在执行 `undo()` 时，如果 `symlinkTarget` 存在，使用 `fs.symlinkSync` 重建链接；对于常规文件，恢复后调用 `fs.chmodSync(absPath, file.mode)` 物理还原执行权限。

### 8.4 缺点 4：全盘覆盖与越权修改冲突 (Lost Manual Updates)
*   **局限性**：Undo 引擎是绝对幂等的。它完全信任 `targetState` 的文件状态清单。
*   **后果**：如果在 Harness 事务录制期间，用户或外部第三方服务意外穿插修改了某几个本不属于该事务的文件，一旦执行 `undo()`，这些**外部手动改写的最新内容也会被直接物理覆盖或物理删除**。
*   **应对措施**：
    1.  **二阶段范围锁定（Scoped Undo）**：允许在 `undo()` 中传入范围过滤器 `scope?: string[]`，限制只回滚特定目录或指定文件，其余文件即使在 targetManifest 里有改动也予以跳过。
    2.  **差异确认拦截（Dirty Check）**：在执行 `undo` 前先做一次脏检查。如果发现某些文件自 `finalState` 捕获后又发生了未知修改，弹窗警告或抛出事务冲突异常，保护手动成果。

### 8.5 缺点 5：与 Git 暂存区步调不一致 (Git Index De-synchronization)
*   **局限性**：此方案运行在 Git 之上且完全独立于 Git Index。
*   **后果**：如果在一轮活动中，AI 或 Harness 自动运行了 `git add file.ts` 把修改塞入了 Git Index，此时你通过 Undo 物理回滚并删除了 `file.ts`，Git 暂存区（Index）里仍然会残留这个文件被修改过的索引痕迹。用户后续执行 `git status` 时会看到一堆奇怪的脏状态（例如 `Changes to be committed: deleted...`）。
*   **应对措施**：
    1.  **物理还原后同步重置（Post-Undo Reset）**：如果检测到当前工作区属于 Git 仓库，在 `undo()` 执行完毕的 `finally` 回调中，静默运行一次 `git reset HEAD <file_paths>`，使 Git Index 与物理实体状态重新对齐。
