import { TurnBoundaryDetector, TerminalOutputEvent, WorkspaceFileChangeEvent, BoundaryDecision } from './TurnBoundaryDetector';

export class QuietWindowBoundaryDetector implements TurnBoundaryDetector {
  private static readonly runningTerminalQuietMs = 12000;
  private static readonly completeGraceMs = 4000;
  private static readonly busyLatchMs = 20000;

  private terminalQuietMs: number;
  private fileQuietMs: number;

  private lastTerminalOutputTime: number = 0;
  private lastFileChangeTime: number = 0;
  private hasActivity: boolean = false;
  private sawCommandEnd: boolean = false;
  private lastPromptTime: number = 0;
  private terminalAwaitingInput: boolean = false;
  private continuationPromptActive: boolean = false;
  private lastBusyTime: number = 0;
  private pendingCompletion?: { reason: string; detectedAt: number };

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
      this.lastBusyTime = 0;
      this.pendingCompletion = undefined;
      return;
    }

    if (event.kind === 'commandEnd') {
      this.sawCommandEnd = true;
      this.terminalAwaitingInput = false;
      this.continuationPromptActive = false;
      this.lastBusyTime = 0;
      this.pendingCompletion = undefined;
      return;
    }

    if (event.kind === 'awaitingInput') {
      this.terminalAwaitingInput = true;
      this.continuationPromptActive = false;
      this.lastPromptTime = 0;
      this.lastBusyTime = 0;
      this.pendingCompletion = undefined;
      return;
    }

    if (event.kind === 'busy') {
      this.lastBusyTime = Date.now();
      this.terminalAwaitingInput = false;
      this.continuationPromptActive = false;
      this.lastPromptTime = 0;
      this.pendingCompletion = undefined;
      return;
    }

    if (event.kind === 'continuationPrompt') {
      this.continuationPromptActive = true;
      this.terminalAwaitingInput = false;
      this.lastPromptTime = 0;
      this.lastBusyTime = 0;
      this.pendingCompletion = undefined;
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
    this.pendingCompletion = undefined;
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
    const busyActive = this.lastBusyTime > 0 && now - this.lastBusyTime < QuietWindowBoundaryDetector.busyLatchMs;
    if (busyActive) {
      this.pendingCompletion = undefined;
      return null;
    }

    const terminalIdleThreshold = this.sawCommandEnd ? this.terminalQuietMs : Math.max(this.terminalQuietMs, QuietWindowBoundaryDetector.runningTerminalQuietMs);
    const terminalIdle = now - this.lastTerminalOutputTime >= terminalIdleThreshold;
    const fileIdle = now - this.lastFileChangeTime >= this.fileQuietMs;

    if (this.sawCommandEnd && this.lastPromptTime > 0 && this.lastPromptTime >= this.lastTerminalOutputTime - 50 && fileIdle) {
      return this.resolvePendingCompletion('Shell prompt restored after command end', now);
    }

    if (terminalIdle && fileIdle) {
      return this.resolvePendingCompletion(
        this.sawCommandEnd ? 'Dual quiet window reached after command end' : 'Dual quiet window reached while command still running',
        now
      );
    }

    this.pendingCompletion = undefined;
    return null;
  }

  private resolvePendingCompletion(reason: string, now: number): BoundaryDecision | null {
    if (!this.pendingCompletion || this.pendingCompletion.reason !== reason) {
      this.pendingCompletion = {
        reason,
        detectedAt: now
      };
      return null;
    }

    if (now - this.pendingCompletion.detectedAt < QuietWindowBoundaryDetector.completeGraceMs) {
      return null;
    }

    return {
      action: 'complete',
      reason
    };
  }

  public reset(): void {
    this.lastTerminalOutputTime = 0;
    this.lastFileChangeTime = 0;
    this.hasActivity = false;
    this.sawCommandEnd = false;
    this.lastPromptTime = 0;
    this.terminalAwaitingInput = false;
    this.continuationPromptActive = false;
    this.lastBusyTime = 0;
    this.pendingCompletion = undefined;
  }

  /**
   * 允许动态修改静默时间窗口
   */
  public updateThresholds(terminalQuietMs: number, fileQuietMs: number): void {
    this.terminalQuietMs = terminalQuietMs;
    this.fileQuietMs = fileQuietMs;
  }
}
