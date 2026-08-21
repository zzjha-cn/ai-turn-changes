import * as vscode from 'vscode';

export class TerminalBindingService {
  private boundTerminal?: vscode.Terminal;
  private onBindingChangedEmitter = new vscode.EventEmitter<vscode.Terminal | undefined>();
  public readonly onBindingChanged = this.onBindingChangedEmitter.event;

  constructor() {
    // 监听终端关闭事件，如果被绑定的终端关闭了，自动解绑
    vscode.window.onDidCloseTerminal(terminal => {
      if (this.boundTerminal && this.boundTerminal === terminal) {
        this.unbind();
      }
    });
  }

  /**
   * 绑定当前活跃的终端
   */
  public bindActiveTerminal(): vscode.Terminal | undefined {
    const activeTerminal = vscode.window.activeTerminal;
    if (activeTerminal) {
      this.boundTerminal = activeTerminal;
      this.onBindingChangedEmitter.fire(activeTerminal);
      return activeTerminal;
    }
    return undefined;
  }

  /**
   * 解绑当前绑定的终端
   */
  public unbind(): void {
    if (this.boundTerminal) {
      this.boundTerminal = undefined;
      this.onBindingChangedEmitter.fire(undefined);
    }
  }

  /**
   * 获取当前被绑定的终端
   */
  public getBoundTerminal(): vscode.Terminal | undefined {
    return this.boundTerminal;
  }

  /**
   * 检查当前终端是否是绑定的活跃终端
   */
  public isBound(terminal: vscode.Terminal): boolean {
    return this.boundTerminal === terminal;
  }
}
