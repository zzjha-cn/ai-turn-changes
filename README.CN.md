# ai-turn-changes

基于轮次获取终端里的Agent任务(如claude-cli)执行情况，展示详细代码变更。通过git检查变更。

![image](./public/dark.png)

- 为什么不用`/diff`命令？
  因为`/diff`命令只能展示当前轮次的变更，不能展示历史轮次的变更。并且，缺乏完整代码逻辑查看，仅看变更内容无法仔细定位。
- 为什么不用git查看变更？
  因为git查看变更只能展示基于commit提交的变更，不能展示终端里的Agent任务里基于轮次的变更，后者粒度更细。
- 扩展依赖git实现变更检查功能。

## 功能
在交互模式下使用cli时，扩展将基于轮次获取终端里的Agent任务执行情况，展示详细代码变更。

- 手动模式(Manual)，每一次手动控制轮次的开始与终止，展示变更。
- 自动模式(Automatic)，根据终端里的Agent任务执行输出，自动判定开始与终止轮次，展示变更。


## 执行原理与链路

- 手动模式原理：
  用户手动控制轮次的开始与终止，基于git展示变更。
- 自动模式原理：
  绑定一个活跃的终端，根据终端里的Agent任务执行输出，自动判定开始与终止轮次，展示变更。
- 如何追踪轮次变更文件？
  通过git检查变更，将对应的git.Object记录为Snapshot，存储在扩展的本地文件夹中（macOs: ~/Library/Application Support/Code/User/globalStorage），利用引用计数管理Snapshot的生命周期。
- 如何判定轮次开始与终止？
  手动模式下通过手动控制。自动模式下，将读取terminal里的输出，识别思考中、等待授权、完成等状态，根据状态自动判定开始与终止轮次。


## 使用方式

- 手动模式(Manual)，每一次手动控制轮次的开始与终止，展示变更。
- 自动模式(Automatic)，绑定一个活跃的终端（Bind Active Terminal），打开自动模式后，自动判定开始与终止轮次。

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
