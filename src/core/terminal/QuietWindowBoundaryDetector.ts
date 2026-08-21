import { TurnBoundaryDetector, TerminalOutputEvent, WorkspaceFileChangeEvent, BoundaryDecision } from './TurnBoundaryDetector';

export class QuietWindowBoundaryDetector implements TurnBoundaryDetector {
  private terminalQuietMs: number;
  private fileQuietMs: number;

  private lastTerminalOutputTime: number = 0;
  private lastFileChangeTime: number = 0;
  private hasActivity: boolean = false;

  constructor(terminalQuietMs: number = 1200, fileQuietMs: number = 1000) {
    this.terminalQuietMs = terminalQuietMs;
    this.fileQuietMs = fileQuietMs;
  }

  public onTerminalOutput(_event: TerminalOutputEvent): void {
    this.lastTerminalOutputTime = Date.now();
    this.hasActivity = true;
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

    const now = Date.now();
    const terminalIdle = now - this.lastTerminalOutputTime >= this.terminalQuietMs;
    const fileIdle = now - this.lastFileChangeTime >= this.fileQuietMs;

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
  }

  /**
   * 允许动态修改静默时间窗口
   */
  public updateThresholds(terminalQuietMs: number, fileQuietMs: number): void {
    this.terminalQuietMs = terminalQuietMs;
    this.fileQuietMs = fileQuietMs;
  }
}
