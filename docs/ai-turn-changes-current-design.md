# AI Turn Changes 当前完整设计文档

## 1. 文档目的

本文档用于沉淀当前项目已经收敛并落地的完整设计，作为现阶段实现与后续演进的统一基线。

与早期设计文档相比，这份文档不再描述最初的探索性方案，而是聚焦当前已经验证过的产品形态、架构分层、关键数据模型、交互方式、已放弃路线与后续演化方向。

本文档覆盖：

- 当前产品目标与定位
- 已实现的用户交互模型
- 当前数据模型与状态流转
- Git、Snapshot、Baseline、Diff、Turn 管理设计
- turn.diff 自定义编辑器设计
- 自动模式骨架设计
- merge 兼容设计
- 配置项与界面约束
- 已废弃方案与原因
- 后续建议演化方向

---

## 2. 当前产品定位

该扩展面向 Claude CLI、Codex CLI 等终端型 AI Coding Agent，目标不是替代 Git，也不是替代 VS Code 内置 diff，而是在编辑器中引入一个新的产品对象：turn。

当前产品定位是：

- 按 turn 组织 AI 改动，而不是按 commit 组织
- 在侧边栏中浏览 turn 与文件变更
- 在自定义 diff 编辑器中查看单文件的增强型 turn diff
- 保留原始源码编辑器纯净，不在真实编辑器中继续注入显式 diff 展示

当前主视图分为两层：

- Turn Explorer：负责轮次管理与文件入口
- turn.diff：负责文件级增强 diff 阅读

---

## 3. 当前产品决策

### 3.1 turn 是第一等对象

扩展中的核心对象不是 Git commit，而是 turn。

turn 的语义是：

- 一次 AI 执行后形成的一组有效代码变更
- 只有实际发生代码变化时，turn 才成立
- `turnId` 只在有效 turn 上递增

### 3.2 Git 只负责边界，不负责产品表达

Git 当前承担的职责是：

- 判断工作区是否为 Git 仓库
- 提供 tracked / untracked 文件边界
- 提供 `.gitignore` 规则过滤
- 辅助文件集合扫描

Git 不再承担的职责是：

- 不再把临时 snapshot 写入用户仓库 `.git/objects`
- 不再作为 turn 视图层的数据来源直接暴露给用户

### 3.3 Snapshot 存储迁移为扩展私有存储

临时文件快照当前存储在扩展私有目录，而不是用户仓库 Git object store。

这样做的原因是：

- 避免污染用户仓库对象库
- 允许扩展自行做生命周期管理
- 方便为 baseline、finalState、merge 重算提供稳定内容引用

### 3.4 turn.diff 是唯一 diff 展示层

当前已明确收敛为：

- 原始编辑器保持纯净
- turn 变更展示统一在 `turn.diff` 自定义编辑器中完成
- 不再维护第二套“真实编辑器内 decoration diff”展示系统

---

## 4. 当前用户体验

## 4.1 侧边栏体验

Activity Bar 中提供扩展入口 `AI Turn Changes`。

侧边栏当前职责：

- 展示当前模式：manual / auto
- 展示终端绑定状态
- 展示 turns 历史列表
- 展示每个 turn 下的变更文件
- 通过节点上下文菜单执行轮次管理操作

当前界面特点：

- 已移除顶部冗余按钮
- 主要操作通过节点点击和右键菜单完成
- 文件节点点击进入 `turn.diff`

### 4.2 手动模式体验

用户流程：

1. 点击开始一轮
2. 扩展记录当前 baseline
3. 用户运行 CLI Agent 修改代码
4. 点击结束一轮
5. 扩展计算当前 turn diff
6. 生成新 turn 并展示在侧边栏中

### 4.3 自动模式体验

当前自动模式处于骨架可用状态：

- 用户可以切换到 auto 模式
- 用户可以绑定活跃终端
- 扩展监听绑定终端与文件静默信号
- 当静默窗口满足条件时，自动形成候选 turn
- 候选 turn 有实际代码变化时才晋升为正式 turn

当前自动监测体验的用户心智是：

- 绑定一个终端后，扩展只关注这个终端所代表的 AI 工作流
- 只要终端与文件系统持续有活动，当前轮就还在进行中
- 当终端输出与文件改动都进入稳定静默后，扩展尝试把这段活动收束成一轮
- 如果这段活动最终没有带来代码变化，就不会生成新的 turn

### 4.4 turn.diff 阅读体验

文件节点点击后打开 `turn.diff` 自定义编辑器，而不是直接打开真实文件。

当前阅读模型为：

- 整体视图保留完整文件代码上下文
- 在变更锚点位置，用一个成对块组件替代正文中的主视觉变更部分
- 成对块采用上下紧凑布局：上 old，下 new
- old / new 采用更深的红绿背景、边框和字符级高亮
- old / new 顶部大标题已去掉，仅保留更弱的小角标
- 支持深色 / 浅色背景主题
- 支持从面板内临时切换主题
- 双击源码行可以跳转到真实文件对应位置

---

## 5. 当前界面结构

### 5.1 Turn Explorer

当前树视图逻辑由 `TreeViewProvider` 负责，分为三类信息：

- Session / 状态节点
- Actions / 可执行操作节点
- Turns / 历史 turn 与文件节点

turn 节点当前支持：

- Select Turn
- Merge Down
- Remove

### 5.2 turn.diff 自定义编辑器

当前 `turn.diff` 使用 `CustomTextEditorProvider` 实现，并使用虚拟 URI：

- scheme：`turn-diff`
- viewType：`turnChanges.diffEditor`

顶部信息区当前包括：

- Turn 编号
- 文件路径
- 当前文件状态
- 主题切换按钮
- 打开真实文件按钮

正文区当前包括：

- 完整代码行
- 成对 diff block
- old / new 行号
- 字符级高亮

### 5.3 主题配置

当前支持两层主题控制：

- 扩展默认配置：`aiTurnChanges.diffEditorTheme`
- 面板内临时切换：仅覆盖当前 webview 状态，不改默认配置

可选值：

- `dark`
- `light`

---

## 6. 当前核心架构

当前核心模块如下：

- `extension.ts`：扩展入口与依赖组装
- `TurnManager`：turn 生命周期与主控制器
- `TurnStore`：turn 持久化与历史管理
- `BaselineStore`：baseline 与 workspace state 构建
- `SnapshotStore`：扩展私有快照存储与引用计数
- `DiffEngine`：状态比较、hunk、inline change、block preview 生成
- `GitWorkspaceService`：Git 仓库边界与 ignore 规则能力
- `TerminalBindingService`：绑定活跃终端
- `TerminalMonitor`：终端事件监听
- `QuietWindowBoundaryDetector`：静默窗口边界判断
- `FileWatchService`：文件变更监听与静默辅助
- `TreeViewProvider`：侧边栏 turn 树
- `TurnDiffEditorProvider`：增强型单文件 diff 视图

---

## 7. 当前数据模型

核心类型定义位于 `src/types/index.ts`。

### 7.1 TurnSession

用于记录扩展当前运行状态：

- `workspaceRoot`
- `mode`
- `nextTurnId`
- `activeCandidate`
- `boundTerminalId`

### 7.2 CandidateTurn

表示尚未正式保存的候选轮次：

- `candidateId`
- `source`
- `startedAt`
- `endedAt`
- `state`
- `baseline`
- `baselineSnapshotIds`
- `terminalContext`

### 7.3 TurnBaseline

表示某一轮开始时的基线文件状态：

- `baselineId`
- `createdAt`
- `files`

### 7.4 WorkspaceState

表示一次完整工作区状态快照：

- `stateId`
- `createdAt`
- `files`

### 7.5 TurnRecord

正式保存的一轮数据：

- `turnId`
- `source`
- `createdAt`
- `terminalContext`
- `parentTurnId`
- `summary`
- `changes`
- `finalState`
- `snapshotIds`

### 7.6 FileChange

单文件变更对象：

- `path`
- `status`
- `previousOid`
- `currentOid`
- `hunks`
- `inlineChanges`
- `blockPreviews`
- `isBinary`

---

## 8. Git 与文件边界设计

当前 Git 能力由 `GitWorkspaceService` 提供。

职责包括：

- 检测当前工作区是否为 Git 仓库
- 枚举 tracked files
- 枚举 untracked files
- 合并得到 turn 候选文件集合
- 使用 `git check-ignore` 统一遵循 `.gitignore`
- 判断文件是否为二进制文件

设计原则：

- Git 负责告诉系统“哪些文件应该被纳入 turn 检查”
- Git 不负责保存 turn 快照本体

---

## 9. Snapshot 存储设计

当前快照存储由 `SnapshotStore` 提供，使用扩展私有存储目录。

### 9.1 存储内容

- 文件内容本体
- 内容哈希作为 snapshotId
- metadata 中的引用计数信息

### 9.2 关键能力

- `storeFileContent()`
- `readContent()`
- `readContentSync()`
- `addReferences()`
- `removeReferences()`

### 9.3 生命周期管理

快照不会无限增长，当前依赖引用计数回收：

- turn 保存后，为其关联 snapshot 增加引用
- active candidate baseline 也会临时持有引用
- turn 删除、合并、历史轮替或 candidate 结束时释放引用
- 引用归零时自动删除 snapshot 文件

这保证了：

- baseline 在 turn 录制期间不会被误删
- merge / remove / history trim 不会导致在用 snapshot 被提前清理

---

## 10. Baseline 与状态比较设计

### 10.1 当前基线策略

当前已不再采用“上一轮 changes 覆盖当前工作区”这一类易漂移方案。

当前稳定策略是：

- 新一轮 baseline 来自上一轮 `finalState`
- 对第一轮，则来自当前工作区完整状态采集

### 10.2 为什么保存 finalState

仅保存 changes 不足以稳定恢复“上一轮结束时整个工作区是什么样子”。

因此 `TurnRecord` 中增加：

- `finalState`

作用是：

- 用于下一轮 baseline 恢复
- 用于 merge 时做 state-to-state 重算
- 防止第三轮再次回漂成全库变更

---

## 11. Diff 计算设计

当前 diff 逻辑由 `DiffEngine` 负责。

### 11.1 输入与输出

输入：

- baseline
- current workspace state

输出：

- `FileChange[]`

### 11.2 当前差异能力

- 文件级 added / modified / deleted 判定
- 文本 hunk 计算
- 二进制文件文件级变化判定
- 行内 inline change 计算
- block preview 生成
- 同步版 compareStatesSync 供 merge 使用

### 11.3 行内变化

为了解决“整段都亮”的问题，当前会额外生成：

- `inlineChanges`

其作用是：

- 让 turn.diff 中可以只高亮真正变更的字符范围
- 为 old/new 成对块提供字符级对照能力

### 11.4 block preview

`blockPreviews` 最初是为原始编辑器内嵌 decoration 准备的。

虽然真实编辑器内联展示已被废弃，但 `blockPreviews` 仍保留为一种中间 diff 表达，可继续用于：

- hunk 锚点辅助
- 后续摘要导航
- 可能的 hover / minimap / jump 视图

---

## 12. Turn 生命周期设计

当前 turn 生命周期主要由 `TurnManager` 负责。

### 12.1 手动模式

- `startManualTurn()`：采集 baseline，创建 candidate
- `finishManualTurn()`：采集 currentState，计算 diff，若有变更则保存 turn
- `discardActiveTurn()`：丢弃 candidate，并释放相关 snapshot 引用

### 12.2 自动模式

自动模式当前已具备基础链路：

- 绑定活跃终端
- 监听终端行为与文件静默
- 建立 candidate
- 静默窗口结束后自动 finalize
- 无变更则自动丢弃 candidate

### 12.3 自动监测轮次的技术原理

当前自动轮次检测并不是直接依赖某个 CLI 提供“本轮结束”事件，而是使用一套弱耦合的活动收敛模型。

这套模型的核心原则是：

- 终端边界判断负责回答“这一段 AI 活动是否可能结束了”
- diff 核对负责回答“这一段活动是否真的形成了代码变更”

两者拆开后，自动模式不会把每一次命令结束都粗暴地变成 turn，也不会依赖特定 provider 的输出格式。

### 12.4 自动监测涉及的模块协作

自动模式当前由以下模块协作完成：

- `TerminalBindingService`：维护当前被绑定的活跃终端
- `TerminalMonitor`：负责监听终端侧活动信号
- `FileWatchService`：负责监听文件系统变化
- `TurnBoundaryDetector`：边界检测抽象接口
- `QuietWindowBoundaryDetector`：当前具体实现，基于静默窗口完成候选轮次判断
- `TurnManager`：负责 candidate 的建立、完成与丢弃

对应的数据流是：

1. 用户绑定活跃终端
2. 终端侧产生输出或命令活动
3. 文件侧产生写入、创建、删除事件
4. 终端与文件活动共同驱动 `QuietWindowBoundaryDetector` 更新内部时钟
5. `TurnManager` 以低频轮询方式调用 `tryCompleteCandidate()`
6. 当检测器判断双静默窗口达成时，触发候选轮次结算
7. 通过 baseline 与 currentState 做真实 diff 核对
8. 有变更则保存 turn，无变更则丢弃 candidate

### 12.5 为什么采用静默窗口模型

当前选择静默窗口，而不是简单使用“命令结束即一轮”，原因是：

- Claude CLI、Codex CLI 等交互式工具并不总能稳定暴露清晰的轮次结束事件
- 用户在同一轮中可能触发多次文件写入、补丁应用或二次修复
- 某些命令虽然结束，但后续文件系统活动仍在继续
- 某些无关命令虽然结束，但并没有真正带来代码变更

因此当前将一轮自动检测定义为：

- 先发生了一段终端或文件活动
- 随后终端进入静默
- 同时文件系统也进入静默
- 静默达到阈值后，才认为这段活动可能完整结束

### 12.6 QuietWindowBoundaryDetector 的工作方式

`QuietWindowBoundaryDetector` 当前是自动模式的默认边界检测器。

它内部维护两类时间戳与状态：

- 最近一次终端活动时间
- 最近一次文件活动时间
- 当前是否已经观察到有效活动

其判断逻辑可以概括为：

- 只要收到终端输出、命令活动或文件变更，就刷新对应的最近活动时间
- 在未观察到任何活动前，不会主动生成 candidate
- 一旦观察到活动，系统进入“可能存在一轮进行中”的状态
- 当 `terminalQuietMs` 与 `fileQuietMs` 两个静默阈值都满足时，返回 `complete`

这意味着它不是一个“命令级”检测器，而是一个“活动收敛级”检测器。

### 12.7 TurnManager 中的自动轮次主链路

自动模式的主控制仍然在 `TurnManager` 中完成。

当前链路如下：

- `setMode('auto')` / `enableAutoMode()`：启用检测器并启动低频轮询
- `checkAutoFinalize()`：周期性询问边界检测器是否满足完成条件
- `startAutoCandidate()`：在需要时创建自动 candidate，并记录 baseline
- `finalizeAutoCandidate()`：采集当前工作区状态、执行 diff，并决定是否保存正式 turn

这里有一个关键设计：

- 自动 candidate 的创建与完成都由 `TurnManager` 控制
- 检测器本身不保存 turn，不读取文件内容，也不做 diff

这样可以保持检测器足够轻量，并允许后续替换成新的边界判断策略。

### 12.8 自动 candidate 的建立时机

当前实现里，candidate 并不会在扩展启动后立即创建，而是遵循按需创建策略。

其建立时机是：

- 检测器已经观察到一段活动
- 并且静默窗口收敛后，需要进行一次 turn 结算
- 如果此时尚未存在 active candidate，则先创建 baseline，再执行 finalize

这种设计的目的在于：

- 避免在没有任何活动时提前占用 snapshot 引用
- 避免将纯空闲状态误解释为一轮
- 让自动模式只在真正发生活动后才进入 candidate 生命周期

### 12.9 文件监听如何参与自动监测

`FileWatchService` 当前不是直接生成 turn，而是给静默检测器提供文件侧活动信号。

它的职责包括：

- 监听工作区文件新增、修改、删除
- 将绝对路径归一化为工作区相对路径
- 使用 `GitWorkspaceService.isIgnored()` 遵循 `.gitignore`
- 忽略不纳入 turn 语义的文件事件
- 在有效文件变化发生时通知检测器刷新文件活动时间

因此，自动模式中的文件监听是“判边界的辅助信号”，不是“直接认定一轮完成的证据”。

### 12.10 终端监听如何参与自动监测

`TerminalMonitor` 当前负责消费与绑定终端相关的事件，并把活动信号传给边界检测器。

它当前的设计意图包括：

- 只关注绑定终端，而不是所有终端
- 为普通命令执行结束场景提供事件入口
- 为未来更细粒度的输出流监听或 prompt 状态识别预留扩展点

这使得当前自动模式可以先用较通用的静默窗口方案落地，再逐步增强到 provider-aware 模式。

### 12.11 自动检测与 diff 核对的两阶段模型

当前自动模式采用典型的两阶段判断：

第一阶段是候选边界检测：

- 通过终端活动与文件活动判断“一段行为是否结束了”

第二阶段是真实变更确认：

- 通过 baseline 与 currentState 的比较，判断“这一段行为是否真的改了代码”

两阶段模型的价值在于：

- 自动模式即使误判了边界，也不会必然生成 turn
- 只有 diff 层确认有实际改动时，turn 才会写入历史
- 无关命令、纯阅读命令、空跑命令最终不会污染 turn 历史

### 12.12 自动模式下的 snapshot 与基线保护

自动模式和手动模式一样，也依赖 baseline snapshot 的稳定可读。

因此当前自动链路中：

- `startAutoCandidate()` 会收集 baseline 中涉及的 snapshotId
- 调用 `snapshotStore.addReferences()` 增加临时引用
- `finalizeAutoCandidate()` 在 `finally` 中释放这些引用

这样可以保证：

- 在自动轮次尚未结算完成前，baseline 不会因历史轮替、remove、merge 等操作被提前回收

### 12.13 当前自动模式的限制

当前自动检测虽然已经可用，但仍属于通用骨架，而不是最终态。

当前限制包括：

- 主要依赖静默窗口，不具备真正的 prompt-aware 语义识别
- 对长时间持续输出、间歇性输出、后台写文件等复杂模式仍可能出现边界不够理想的情况
- 尚未针对 Claude CLI、Codex CLI 的不同交互特征做专门适配

因此当前自动模式的定位仍然是：

- 一个可用、可扩展、低耦合的自动轮次检测底座

### 12.14 snapshot 引用保护

为避免 `Snapshot not found`，当前 candidate baseline 也会单独持有：

- `baselineSnapshotIds`

并在：

- finish
- discard
- auto finalize

阶段通过 `try/finally` 保证释放。

### 12.15 自动模式下的场景化判定说明

为了避免将自动模式误解为“每次文件修改就是一轮”或“每次命令结束就是一轮”，这里补充几个典型场景。

#### 场景 A：交互式 CLI 连续工作流

例如：

- 模型思考
- 修改文件
- 思考
- 查询
- 修改文件

在当前自动模式语义下，这类链路默认更倾向于被判定为：

- 一次 turn

原因是：

- 自动模式看的是一整段 AI 活动是否已经收敛结束
- 只要绑定终端仍持续产生有效活动，或者文件侧仍持续发生有效改动，系统就认为当前轮仍在进行中
- 中间即使出现多次文件修改，也不会立刻切分新 turn

因此，如果“第一次修改文件”与“第二次修改文件”之间，终端侧与文件侧都没有同时进入并持续满足静默窗口，那么最终会在整段活动结束后统一结算为一轮。

#### 场景 B：两段活动之间出现稳定静默

例如：

- 模型思考
- 修改文件
- CLI 回到可输入状态
- 终端与文件都持续静默并超过阈值
- 用户再次发起查询
- 再次修改文件

这类链路更可能被判定为：

- 两次 turn

原因是：

- 第一段活动结束后，系统已经观察到终端静默与文件静默同时达标
- `TurnManager` 会触发一次 finalize
- 后续再次出现新的终端活动与文件活动时，会被视为新的候选轮次

也就是说，自动模式真正的切分边界不是“文件改了几次”，而是“上一段活动是否已经完成收敛并被系统正式结算”。

#### 场景 C：第一次改完后，模型还在继续思考或查询

例如：

- 修改文件
- 模型继续输出分析
- CLI 继续查询上下文
- 稍后再次修改文件

这类场景当前应当继续算作：

- 同一次 turn

因为从系统角度看，终端活动链路尚未结束，文件修改只是这段连续 AI 工作流中的中间动作，而不是天然的轮次边界。

#### 场景 D：中间停顿较长，但终端活动信号不充分

当前自动模式虽然设计目标是按“活动收敛”切分，但实际效果仍受终端活动捕捉粒度影响。

因此存在一种边界情况：

- 交互式 CLI 进入较长思考或等待阶段
- 这段时间文件也没有继续变化
- 如果终端侧没有被系统识别为持续活动
- 并且静默时间超过阈值

那么系统可能会提前把前半段结算成一个 turn，导致后续再次修改文件时落入下一轮。

这也是为什么当前文档将自动模式定位为：

- 可用的通用骨架
- 但还不是最终的 provider-aware prompt 语义识别方案

#### 当前结论

对于类似“模型思考 → 修改文件 → 思考 → 查询 → 修改文件”的交互式 CLI 场景，当前产品语义上的期望是：

- 如果终端仍处于明确的交互等待态，例如权限确认、执行确认、选项选择或 shell continuation prompt，即使暂时没有新的文件改动，也不应立即结束当前 turn
- 如果终端已经明确回到 shell prompt，且文件侧也已收敛，则应优先判定当前活动段已经结束
- 当无法明确识别终端语义状态时，仍以稳定双静默窗口作为兜底完成条件
- 只有当前一段活动已经收敛并完成 finalize，后续新活动才应进入下一次 turn

因此，自动模式本质上更接近：

- 按连续 AI 活动段切分 turn
- 结合终端 prompt-aware 信号优先判断边界

而不是：

- 按每次文件修改切分
- 按每次命令返回切分

---

## 13. merge 设计

### 13.1 用户语义

当前支持 turn 向下合并。

语义不是简单拼接 changes，而是：

- older turn 的 parent baseline
- 对比 newer turn 的 finalState
- 重新计算 merged turn 的完整 diff

### 13.2 当前实现原则

merge 后必须重算：

- `changes`
- `hunks`
- `inlineChanges`
- `blockPreviews`

这样才能保证 merge 后的 turn.diff 视图仍然正确。

### 13.3 与 turn.diff 的兼容

当前 `turn.diff` 消费的是：

- `previousOid`
- `currentOid`
- `hunks`
- `inlineChanges`

而 merge 后这些数据都会被重算，所以当前 turn.diff 已兼容 merged turn。

---

## 14. turn.diff 渲染设计

当前 `TurnDiffEditorProvider` 是最重要的产品层视图模块。

### 14.1 为什么不继续用真实编辑器 decoration

已验证原生 decoration 存在明显上限：

- 只能附加文本，不能真正插入复杂容器
- 很难做出与 Trae 类似的块级 old/new 对照
- 内容容易挤在同一行尾部
- 难以同时保留完整文件与清晰 diff 阅读体验

因此当前路线改为：

- 用自定义编辑器模拟增强型源码视图

### 14.2 当前渲染模型

当前渲染模型不是传统双栏 diff，而是：

- 以当前文件完整内容作为主上下文
- 在变更位置用成对块组件表达 old/new
- 非变更代码继续按普通源码行渲染
- 变更正文不再与 old/new 同时重复出现，避免视觉混乱

### 14.3 当前成对块结构

每个变更单元由以下部分构成：

- pair summary
- old block
- connector
- new block

视觉要求：

- 上 old 下 new
- 紧凑上下布局
- old/new 角标弱提示
- old/new 颜色更深以增强区分

### 14.4 主题策略

当前主题策略：

- 默认主题由扩展配置提供
- 面板内可临时切换
- 临时切换不影响用户默认配置

### 14.5 跳转能力

当前支持在 turn.diff 中双击真实源码行：

- 打开真实文件
- 跳转到对应行

---

## 15. 终端自动化骨架设计

当前自动化相关模块包括：

- `TerminalBindingService`
- `TerminalMonitor`
- `TurnBoundaryDetector`
- `QuietWindowBoundaryDetector`
- `FileWatchService`

### 15.1 绑定策略

- 只绑定用户当前选择的活跃终端
- 不监听所有终端，避免噪音过大

### 15.2 边界检测策略

当前自动切分策略为：

- 终端输出静默
- 文件静默

两者同时满足时，视为候选轮次可结束。

这是当前自动模式的 MVP 边界判断方式。

进一步说，当前技术判据是：

- 系统先观察到一段有效活动
- 终端侧不再出现新的输出或执行活动
- 文件侧不再出现新的有效改动事件
- 两个静默窗口都超过阈值后，检测器才返回完成信号

这保证自动轮次不会仅因为单侧静默就过早截断。

### 15.3 监听边界与差异确认分离

自动化层只负责候选边界检测。

最终 turn 是否成立，仍然由：

- baseline
- currentState
- diff compare

共同决定。

这也是当前自动模式可靠性的核心来源：

- 检测器只负责边界猜测
- TurnManager 负责生命周期管理
- DiffEngine 负责最终事实确认

三者职责明确分离，后续可分别替换和增强。

---

## 16. 当前配置项

当前显式提供的配置项：

- `aiTurnChanges.diffEditorTheme`

含义：

- 设置 turn.diff 默认背景主题

可选值：

- `dark`
- `light`

默认值：

- `dark`

---

## 17. 当前已放弃的路线

### 17.1 Git object 直接写入用户仓库

已放弃，原因：

- 会污染用户仓库
- 生命周期不可控
- 不适合作为扩展内部临时状态存储

### 17.2 基于 changes 拼下一轮 baseline

已放弃，原因：

- 容易在多轮后漂移
- 会导致第三轮重新变成全库变更

### 17.3 原始编辑器内嵌 decoration diff

已放弃，原因：

- 无法真正复刻 Trae 风格
- old/new 块在真实编辑器里难以形成高质量布局
- 与独立的 turn.diff 方案相比维护成本更高、效果更弱

---

## 18. 当前实现边界

当前实现已经可用，但仍有明确边界：

- 自动模式仍以静默窗口为主，未做 provider-aware 的 prompt 边界识别
- turn.diff 虽然已接近增强源码视图，但还不是完整自定义 IDE 级渲染器
- 当前未支持更复杂的上下文折叠、块折叠、块导航目录
- 当前未提供按 turn 的回滚 patch 能力

---

## 19. 后续建议演化

建议后续按以下方向继续推进：

### 19.1 turn.diff 继续产品化

- 增加上下文折叠
- 增加变更目录导航
- 增加块级快速跳转
- 增加 hover 说明与键盘导航

### 19.2 自动模式增强

- 更精细的终端输出边界检测
- 交互式 CLI 回到 prompt 状态识别
- 面向 Claude / Codex 的 provider-aware 适配

### 19.3 数据层增强

- 更细的 snapshot 统计与清理策略
- turn 搜索、过滤、标签能力
- 变更摘要自动生成

---

## 20. 当前结论

当前项目已经从“在真实编辑器里打 diff 标记”的探索阶段，收敛为更稳定的方案：

- turn 作为核心对象
- Git 负责边界
- SnapshotStore 负责扩展私有快照与生命周期
- finalState 负责稳定轮次比较
- TurnManager 负责 turn 生命周期与 merge 语义
- turn.diff 自定义编辑器负责文件级增强 diff 展示

这是一套当前已经验证可行、并且产品体验明显优于终端 diff 与普通 Git diff 的完整设计。
