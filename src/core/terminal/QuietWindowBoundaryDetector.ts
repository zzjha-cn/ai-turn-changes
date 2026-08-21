import { TurnBoundaryDetector, TerminalOutputEvent, WorkspaceFileChangeEvent, BoundaryDecision } from './TurnBoundaryDetector';

export class QuietWindowBoundaryDetector implements TurnBoundaryDetector {
  private terminalQuietMs: number;
  private fileQuietMs: number;

  private lastTerminalOutputTime: number = 0;
  private lastFileChangeTime: number = 0;
  private hasActivity: boolean = false;
  private sawCommandEnd: boolean = false;
  private lastPromptTime: number = 0;
  private terminalAwaitingInput: boolean = false;
  private continuationPromptActive: boolean = false;

  constructor(terminalQuietMs: number = 1200, fileQuietMs: number = 1000) {
    this.terminalQuietMs = terminalQuietMs;
    this.fileQuietMs = fileQuietMs;
  }

  public onTerminalOutput(event: TerminalOutputEvent): void {
    this.lastTerminalOutputTime = Date.now();
    this.hasActivity = true;

    if (event.kind === 'commandStart') {
      this.sawCommandEnd = false;
      this.lastPromptTime = 0;
      this.terminalAwaitingInput = false;
      this.continuationPromptActive = false;
      return;
    }

    if (event.kind === 'commandEnd') {
      this.sawCommandEnd = true;
      this.terminalAwaitingInput = false;
      this.continuationPromptActive = false;
      return;
    }

    if (event.kind === 'awaitingInput') {
      this.terminalAwaitingInput = true;
      this.continuationPromptActive = false;
      this.lastPromptTime = 0;
      return;
    }

    if (event.kind === 'continuationPrompt') {
      this.continuationPromptActive = true;
      this.terminalAwaitingInput = false;
      this.lastPromptTime = 0;
      return;
    }

    if (event.kind === 'prompt') {
      this.lastPromptTime = Date.now();
      this.terminalAwaitingInput = false;
      this.continuationPromptActive = false;
      return;
    }

    this.terminalAwaitingInput = false;
    this.continuationPromptActive = false;
  }

  public onFileChange(_event: WorkspaceFileChangeEvent): void {
    this.lastFileChangeTime = Date.now();
    this.hasActivity = true;
  }

  /**
   * 尝试判定当前候选轮次是否结束。
   * 当终端和文件在设定的 quiet ms 内均没有任何活动，且曾经发生过活动时，判定为结束。
   */
  public tryCompleteCandidate(): BoundaryDecision | null {
    if (!this.hasActivity) {
      return null;
    }

    if (this.terminalAwaitingInput || this.continuationPromptActive) {
      return null;
    }

    const now = Date.now();
    const terminalIdle = now - this.lastTerminalOutputTime >= this.terminalQuietMs;
    const fileIdle = now - this.lastFileChangeTime >= this.fileQuietMs;

    if (this.sawCommandEnd && this.lastPromptTime > 0 && this.lastPromptTime >= this.lastTerminalOutputTime - 50 && fileIdle) {
      return {
        action: 'complete',
        reason: 'Shell prompt restored after command end'
      };
    }

    if (terminalIdle && fileIdle) {
      return {
        action: 'complete',
        reason: 'Dual quiet window reached'
      };
    }

    return null;
  }

  public reset(): void {
    this.lastTerminalOutputTime = 0;
    this.lastFileChangeTime = 0;
    this.hasActivity = false;
    this.sawCommandEnd = false;
    this.lastPromptTime = 0;
    this.terminalAwaitingInput = false;
    this.continuationPromptActive = false;
  }

  /**
   * 允许动态修改静默时间窗口
   */
  public updateThresholds(terminalQuietMs: number, fileQuietMs: number): void {
    this.terminalQuietMs = terminalQuietMs;
    this.fileQuietMs = fileQuietMs;
  }
}
