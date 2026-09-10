# ai-turn-changes

基于轮次获取终端里的Agent任务(如claude-cli)执行情况，展示详细代码变更。通过git检查变更。

![image](./public/dark.png)

## 宿主能力差异

- Trae：支持完整自动模式能力，可结合终端实时输出、终端状态与文件变化，自动判定 turn 边界。
- VS Code：当前建议以手动模式为主。根本原因是标准 VS Code 发布版不会把与 Trae 同等级别的终端实时输出能力，作为普通已发布扩展的稳定基础能力开放出来。
- 在这个项目里，完整自动轮次判断依赖读取 terminal 输出，并进一步识别思考中、等待授权、progress 摘要、prompt 恢复以及静默窗口等状态。Trae 可以提供这条能力链路；标准 VS Code 发布版默认无法让普通扩展稳定依赖这类终端字符流能力。
- 这不是业务逻辑差异，而是宿主 API 能力差异。项目当前维持：Trae 全功能、VS Code 稳定优先。

### VS Code 增强模式

- 如果你本身就是开发者，希望在 VS Code 中启用更完整的 terminal-aware 自动模式，可以显式打开 proposed API。
- 如果你构建自己的扩展变体，可在扩展包中声明：`"enabledApiProposals": ["terminalDataWriteEvent"]`
- 然后使用以下方式启动 VS Code：

```bash
code . --enable-proposed-api seanz-hahaha.ai-turn-changes
```

- 启用后，扩展会尝试使用与 Trae 更接近的终端状态识别能力。
- 如果当前宿主或启动方式仍不允许，扩展会自动降级到稳定优先路径，不会直接失效。

- 为什么不用`/diff`命令？
  因为`/diff`命令只能展示当前轮次的变更，不能展示历史轮次的变更。并且，缺乏完整代码逻辑查看，仅看变更内容无法仔细定位。
- 为什么不用git查看变更？
  因为git查看变更只能展示基于commit提交的变更，不能展示终端里的Agent任务里基于轮次的变更，后者粒度更细。
- 扩展依赖git实现变更检查功能。

## 功能
在交互模式下使用cli时，扩展将基于轮次获取终端里的Agent任务执行情况，展示详细代码变更。

- 手动模式(Manual)，每一次手动控制轮次的开始与终止，展示变更。
- 自动模式(Automatic)，在 Trae 中可根据终端里的Agent任务执行输出自动判定开始与终止轮次；在标准 VS Code 中当前不作为主推荐用法。


## 执行原理与链路

- 手动模式原理：
  用户手动控制轮次的开始与终止，基于git展示变更。
- 自动模式原理：
  绑定一个活跃的终端，根据终端里的Agent任务执行输出，自动判定开始与终止轮次，展示变更。该能力在 Trae 中完整可用；标准 VS Code 发布版因宿主终端 API 限制，无法完整使用同等级别的终端实时状态识别。
- 如何追踪轮次变更文件？
  通过git检查变更，将对应的git.Object记录为Snapshot，存储在扩展的本地文件夹中（macOs: ~/Library/Application Support/Code/User/globalStorage），利用引用计数管理Snapshot的生命周期。
- 如何判定轮次开始与终止？
  手动模式下通过手动控制。自动模式下，Trae 版本会读取 terminal 输出并识别思考中、等待授权、完成等状态，再结合文件变化自动判定开始与终止轮次；VS Code 发布版当前建议使用手动触发，以保证行为稳定可预期。


## 使用方式

- Trae：优先推荐自动模式(Automatic)。绑定一个活跃的终端（Bind Active Terminal）后，可自动判定开始与终止轮次。
- VS Code：当前优先推荐手动模式(Manual)。每一次手动控制轮次的开始与终止，行为更稳定，也更符合标准 VS Code 发布版的宿主能力边界。
- VS Code 增强模式：如果你明确使用 `--enable-proposed-api seanz-hahaha.ai-turn-changes` 启动 VS Code，也可以实验性启用更完整的自动模式；但默认推荐仍然是手动模式，以保证行为稳定可预期。

### 插件面板功能说明
- Session: 展示当前状态，如模式选择、终端绑定情况。
- Actions:
    - Start Turn: 开始当前轮次的变更展示。
    - End Turn: 结束当前轮次的变更展示。
    - Switch Mode: 切换到手动模式或自动模式。
    - Bind Active Terminal: 绑定当前活跃的终端到自动模式。
- Turns: 展示当前轮次的变更内容。

## 开发

1. `npm install`
2. `npm run compile`
3. 在 VSCode 中按 F5 启动扩展开发主机

## 许可证

MIT License
