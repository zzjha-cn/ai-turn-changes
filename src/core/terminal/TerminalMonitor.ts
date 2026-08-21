import * as vscode from 'vscode';
import { TerminalBindingService } from './TerminalBindingService';
import { TurnBoundaryDetector } from './TurnBoundaryDetector';

export class TerminalMonitor {
  private bindingService: TerminalBindingService;
  private detector: TurnBoundaryDetector;
  private disposables: vscode.Disposable[] = [];
  private terminalTailBuffer = new Map<string, string>();

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
          this.detector.onTerminalOutput({
            terminal: e.terminal,
            data: `[Command Started]: ${e.execution?.commandLine?.value || ''}`,
            kind: 'commandStart'
          });
        }
      });
      this.disposables.push(startExecutionDisposable);
    }

    if ('onDidEndTerminalShellExecution' in (vscode.window as any)) {
      const endExecutionDisposable = (vscode.window as any).onDidEndTerminalShellExecution((e: any) => {
        const boundTerminal = this.bindingService.getBoundTerminal();
        if (boundTerminal && e.terminal === boundTerminal) {
          this.detector.onTerminalOutput({
            terminal: e.terminal,
            data: '[Command Ended]',
            kind: 'commandEnd'
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
        const kind = this.classifyTerminalData(e.terminal, e.data);
        this.detector.onTerminalOutput({
          terminal: e.terminal,
          data: e.data,
          kind
        });
      }
    });

    if (writeDataDisposable) {
      this.disposables.push(writeDataDisposable);
    }
  }

  public dispose(): void {
    this.terminalTailBuffer.clear();
    this.disposables.forEach(d => d.dispose());
  }

  private classifyTerminalData(terminal: vscode.Terminal, data: string): 'output' | 'prompt' | 'awaitingInput' | 'continuationPrompt' {
    const key = this.getTerminalKey(terminal);
    const previous = this.terminalTailBuffer.get(key) || '';
    const combined = (previous + data).slice(-4000);
    this.terminalTailBuffer.set(key, combined);

    const normalized = combined.replace(/\x1b\[[0-9;?]*[ -/]*[@-~]/g, '');
    const lines = normalized.split(/\r?\n/);
    const tailLine = (lines[lines.length - 1] || '').trimEnd();
    const recentTail = normalized.slice(-600);

    if (this.isAwaitingInput(recentTail, tailLine)) {
      return 'awaitingInput';
    }
    if (this.isContinuationPrompt(tailLine)) {
      return 'continuationPrompt';
    }
    if (this.isShellPrompt(tailLine)) {
      return 'prompt';
    }
    return 'output';
  }

  private isAwaitingInput(recentTail: string, tailLine: string): boolean {
    const promptPatterns = [
      /(?:^|\n).*\b(?:allow|approve|grant permission|proceed|continue|confirm|apply)\b[^\n]*\?/i,
      /(?:^|\n).*\b(?:select|choose|pick)\b[^\n]*\b(?:option|an option|one)\b/i,
      /(?:^|\n).*\bpress\s+(?:enter|return)\s+to\s+(?:continue|confirm)\b/i,
      /(?:^|\n).*\b(?:waiting for input|awaiting input|requires confirmation)\b/i,
      /(?:^|\n).*\[(?:y\/n|yes\/no|Y\/n|y\/N|1\/2|1-9)\]\s*$/i,
      /(?:^|\n).*\((?:y\/n|yes\/no|Y\/n|y\/N)\)\s*$/i,
      /(?:^|\n).*[:：]\s*$/
    ];
    if (promptPatterns.some(pattern => pattern.test(recentTail))) {
      return true;
    }
    return /(?:^|\s)(?:y\/n|yes\/no)\s*\??\s*$/i.test(tailLine);
  }

  private isContinuationPrompt(tailLine: string): boolean {
    return /(?:^|\s)(?:dquote|quote|bquote|pipe|cmdsubst|heredoc)>\s*$/.test(tailLine);
  }

  private isShellPrompt(tailLine: string): boolean {
    return /(?:^|\s)(?:\$|%|#|❯)\s*$/.test(tailLine)
      || /^PS [^\r\n>]+>\s*$/.test(tailLine)
      || /(?:^|\s)(?:>|➜)\s*$/.test(tailLine);
  }

  private getTerminalKey(terminal: vscode.Terminal): string {
    return `${terminal.name}:${terminal.creationOptions?.name || ''}`;
  }
}
