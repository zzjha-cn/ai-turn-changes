import * as vscode from 'vscode';
import { TerminalBindingService } from './TerminalBindingService';
import { TurnBoundaryDetector } from './TurnBoundaryDetector';

export class TerminalMonitor {
  private bindingService: TerminalBindingService;
  private detector: TurnBoundaryDetector;
  private disposables: vscode.Disposable[] = [];

  constructor(bindingService: TerminalBindingService, detector: TurnBoundaryDetector) {
    this.bindingService = bindingService;
    this.detector = detector;

    this.initTerminalListener();
  }

  /**
   * 监听并捕获绑定终端的输出以及 Shell 命令执行信号
   */
  private initTerminalListener(): void {
    // 1. 利用官方稳定的 Shell Execution 机制监听命令启动和结束
    // 每次命令执行（包括交互式命令中的单次提问，如果终端开启了 shell integration）都会产生 ShellExecution 事件
    if ('onDidStartTerminalShellExecution' in (vscode.window as any)) {
      const startExecutionDisposable = (vscode.window as any).onDidStartTerminalShellExecution((e: any) => {
        const boundTerminal = this.bindingService.getBoundTerminal();
        if (boundTerminal && e.terminal === boundTerminal) {
          // 产生一个微小事件，告知检测器终端开始活动了
          this.detector.onTerminalOutput({
            terminal: e.terminal,
            data: `[Command Started]: ${e.execution?.commandLine?.value || ''}`
          });
        }
      });
      this.disposables.push(startExecutionDisposable);
    }

    if ('onDidEndTerminalShellExecution' in (vscode.window as any)) {
      const endExecutionDisposable = (vscode.window as any).onDidEndTerminalShellExecution((e: any) => {
        const boundTerminal = this.bindingService.getBoundTerminal();
        if (boundTerminal && e.terminal === boundTerminal) {
          // 终端命令执行结束，触发静默检测的起点
          this.detector.onTerminalOutput({
            terminal: e.terminal,
            data: '[Command Ended]'
          });
        }
      });
      this.disposables.push(endExecutionDisposable);
    }

    // 2. 兼容性降级监听：监听终端数据的写入 (支持标准 PTY 劫持)
    // VS Code 在有权限或特定平台下可通过 onDidWriteTerminalData 稳定捕获实时字符流
    const writeDataDisposable = (vscode.window as any).onDidWriteTerminalData?.((e: any) => {
      const boundTerminal = this.bindingService.getBoundTerminal();
      if (boundTerminal && e.terminal === boundTerminal) {
        this.detector.onTerminalOutput({
          terminal: e.terminal,
          data: e.data
        });
      }
    });

    if (writeDataDisposable) {
      this.disposables.push(writeDataDisposable);
    }
  }

  public dispose(): void {
    this.disposables.forEach(d => d.dispose());
  }
}
