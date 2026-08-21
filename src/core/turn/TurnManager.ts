import * as vscode from 'vscode';
import { CandidateTurn, TurnRecord, TurnBaseline, WorkspaceState } from '../../types';
import { GitWorkspaceService } from '../git/GitWorkspaceService';
import { BaselineStore } from '../baseline/BaselineStore';
import { DiffEngine } from '../diff/DiffEngine';
import { SnapshotStore } from '../storage/SnapshotStore';
import { TurnStore } from '../storage/TurnStore';
import { TurnBoundaryDetector } from '../terminal/TurnBoundaryDetector';

export class TurnManager {
  private gitService: GitWorkspaceService;
  private baselineStore: BaselineStore;
  private diffEngine: DiffEngine;
  private snapshotStore: SnapshotStore;
  private turnStore: TurnStore;

  private activeCandidate?: CandidateTurn;
  private mode: 'manual' | 'auto' = 'manual';
  private boundaryDetector?: TurnBoundaryDetector;
  private autoFinalizeTimer?: NodeJS.Timeout;

  private onStateChangedEmitter = new vscode.EventEmitter<void>();
  public readonly onStateChanged = this.onStateChangedEmitter.event;

  constructor(
    gitService: GitWorkspaceService,
    baselineStore: BaselineStore,
    diffEngine: DiffEngine,
    snapshotStore: SnapshotStore,
    turnStore: TurnStore,
    _workspaceRoot: string
  ) {
    this.gitService = gitService;
    this.baselineStore = baselineStore;
    this.diffEngine = diffEngine;
    this.snapshotStore = snapshotStore;
    this.turnStore = turnStore;
  }

  /**
   * 设定边界检测器，并激活自动化模式
   */
  public enableAutoMode(detector: TurnBoundaryDetector): void {
    this.mode = 'auto';
    this.boundaryDetector = detector;
    this.boundaryDetector.reset();

    // 启动一个低频轮询，用来检查静默窗口是否满足
    if (this.autoFinalizeTimer) {
      clearInterval(this.autoFinalizeTimer);
    }
    this.autoFinalizeTimer = setInterval(() => {
      this.checkAutoFinalize();
    }, 500);

    this.onStateChangedEmitter.fire();
  }

  public setMode(mode: 'manual' | 'auto', detector?: TurnBoundaryDetector): void {
    if (mode === 'auto') {
      if (!detector && !this.boundaryDetector) {
        throw new Error('Auto mode requires a boundary detector.');
      }
      this.enableAutoMode(detector || this.boundaryDetector!);
      return;
    }
    this.disableAutoMode();
  }

  /**
   * 关闭自动化模式回到手动模式
   */
  public disableAutoMode(): void {
    this.mode = 'manual';
    this.boundaryDetector = undefined;
    if (this.autoFinalizeTimer) {
      clearInterval(this.autoFinalizeTimer);
      this.autoFinalizeTimer = undefined;
    }
    this.onStateChangedEmitter.fire();
  }

  /**
   * 检查自动模式下，是否由于检测到双静默窗口而需要生成一轮新 Turn
   */
  private async checkAutoFinalize(): Promise<void> {
    if (this.mode !== 'auto' || !this.boundaryDetector) {
      return;
    }

    const decision = this.boundaryDetector.tryCompleteCandidate();
    if (decision && decision.action === 'complete') {
      // 1. 如果当前没有活跃的 Candidate 记录，则在触发检测到活动时，自动创建 baseline 并进入 recording 状态
      // 由于 QuietWindowBoundaryDetector 在第一次收到输出或文件改动时，就会有 hasActivity
      // 我们需要在静默达成时执行 Finalize。
      if (!this.activeCandidate) {
        // 自动产生一个 Candidate
        await this.startAutoCandidate();
      }

      // 执行 Finalize
      await this.finalizeAutoCandidate();
    }
  }

  /**
   * 基于上一个已保存轮次的最终状态，或者是当前磁盘物理状态，混合生成本轮的 OID 增量基线。
   * 确保两轮之间完美的增量比对。
   */
  private async createTurnIncrementalBaseline(): Promise<TurnBaseline> {
    const history = this.turnStore.getHistory();
    if (history.length === 0) {
      return this.baselineStore.createBaseline();
    }

    const lastTurn = this.getLatestTurn(history);
    if (!lastTurn?.finalState) {
      return this.baselineStore.createBaseline();
    }
    return this.baselineStore.createBaselineFromWorkspaceState(lastTurn.finalState);
  }

  /**
   * 自动开始候选轮次 ( 捕获静默之前的初始状态 )
   */
  private async startAutoCandidate(): Promise<void> {
    const isGit = await this.gitService.isGitRepository();
    if (!isGit) { return; }

    const baseline = await this.createTurnIncrementalBaseline();
    const baselineSnapshotIds = this.collectBaselineSnapshotIds(baseline);
    this.snapshotStore.addReferences(baselineSnapshotIds);
    this.activeCandidate = {
      candidateId: 'candidate_' + Date.now(),
      source: 'command',
      startedAt: Date.now(),
      state: 'recording',
      baseline,
      baselineSnapshotIds
    };
    this.onStateChangedEmitter.fire();
  }

  /**
   * 自动结算并核对是否有代码改动，若无则自动静默丢弃
   */
  private async finalizeAutoCandidate(): Promise<void> {
    if (!this.activeCandidate) { return; }

    this.activeCandidate.state = 'settling';
    const baseline = this.activeCandidate.baseline;
    const baselineSnapshotIds = this.activeCandidate.baselineSnapshotIds || [];
    this.activeCandidate = undefined; // 立即置空，防止重入
    this.boundaryDetector?.reset();

    try {
      const currentState = await this.diffEngine.captureCurrentWorkspaceState();
      const changes = await this.diffEngine.compareStates(baseline, currentState);
      if (changes.length > 0) {
        const turnId = this.turnStore.getNextTurnId();
        let insertions = 0;
        let deletions = 0;
        for (const change of changes) {
          if (change.hunks) {
            for (const hunk of change.hunks) {
              for (const line of hunk.lines) {
                if (line.startsWith('+')) { insertions++; }
                else if (line.startsWith('-')) { deletions++; }
              }
            }
          }
        }

        const record: TurnRecord = {
          turnId,
          source: 'auto',
          createdAt: Date.now(),
          parentTurnId: this.getLatestTurnId(),
          summary: {
            filesChanged: changes.length,
            insertions,
            deletions
          },
          changes,
          finalState: currentState,
          snapshotIds: this.collectSnapshotIds(changes, currentState)
        };

        this.turnStore.saveTurn(record);
        this.snapshotStore.addReferences(record.snapshotIds || []);
        this.cleanupRemovedTurnSnapshots();
        this.turnStore.incrementNextTurnId(turnId);

      }
    } finally {
      this.snapshotStore.removeReferences(baselineSnapshotIds);
    }

    this.onStateChangedEmitter.fire();
  }

  /**
   * 获取当前运行模式
   */
  public getMode(): 'manual' | 'auto' {
    return this.mode;
  }


  /**
   * 手动开始新一轮
   */
  public async startManualTurn(): Promise<void> {
    if (this.activeCandidate) {
      throw new Error('Already have an active turn recording. Please finish or discard it first.');
    }

    const isGit = await this.gitService.isGitRepository();
    if (!isGit) {
      throw new Error('Workspace is not a valid Git repository.');
    }

    const baseline = await this.createTurnIncrementalBaseline();
    const baselineSnapshotIds = this.collectBaselineSnapshotIds(baseline);
    this.snapshotStore.addReferences(baselineSnapshotIds);
    this.activeCandidate = {
      candidateId: 'candidate_' + Date.now(),
      source: 'manual',
      startedAt: Date.now(),
      state: 'recording',
      baseline,
      baselineSnapshotIds
    };

    this.onStateChangedEmitter.fire();
  }

  /**
   * 手动结束当前轮次，并根据差异决定是否保留
   */
  public async finishManualTurn(): Promise<TurnRecord | null> {
    if (!this.activeCandidate) {
      throw new Error('No active turn is recording.');
    }

    this.activeCandidate.state = 'settling';
    this.onStateChangedEmitter.fire();

    const activeCandidate = this.activeCandidate;
    const baselineSnapshotIds = activeCandidate.baselineSnapshotIds || [];
    try {
      const currentState = await this.diffEngine.captureCurrentWorkspaceState();
      const changes = await this.diffEngine.compareStates(activeCandidate.baseline, currentState);
      if (changes.length === 0) {
        this.activeCandidate = undefined;
        this.onStateChangedEmitter.fire();
        return null;
      }

      const turnId = this.turnStore.getNextTurnId();

      let insertions = 0;
      let deletions = 0;
      for (const change of changes) {
        if (change.hunks) {
          for (const hunk of change.hunks) {
            for (const line of hunk.lines) {
              if (line.startsWith('+')) {
                insertions++;
              } else if (line.startsWith('-')) {
                deletions++;
              }
            }
          }
        }
      }

      const record: TurnRecord = {
        turnId,
        source: 'manual',
        createdAt: Date.now(),
        parentTurnId: this.getLatestTurnId(),
        summary: {
          filesChanged: changes.length,
          insertions,
          deletions
        },
        changes,
        finalState: currentState,
        snapshotIds: this.collectSnapshotIds(changes, currentState)
      };

      this.turnStore.saveTurn(record);
      this.snapshotStore.addReferences(record.snapshotIds || []);
      this.cleanupRemovedTurnSnapshots();
      this.turnStore.incrementNextTurnId(turnId);

      this.activeCandidate = undefined;
      this.onStateChangedEmitter.fire();
      return record;
    } finally {
      this.snapshotStore.removeReferences(baselineSnapshotIds);
    }
  }

  /**
   * 丢弃当前录制中的轮次
   */
  public discardActiveTurn(): void {
    if (this.activeCandidate) {
      this.snapshotStore.removeReferences(this.activeCandidate.baselineSnapshotIds || []);
      this.activeCandidate = undefined;
      this.onStateChangedEmitter.fire();
    }
  }

  /**
   * 获取当前录制中的 CandidateTurn
   */
  public getActiveCandidate(): CandidateTurn | undefined {
    return this.activeCandidate;
  }

  /**
   * 获取历史 TurnRecord 列表
   */
  public getHistory(): TurnRecord[] {
    return this.turnStore.getHistory();
  }

  public removeTurn(turnId: number): TurnRecord | null {
    const history = this.turnStore.getHistory();
    const target = history.find(record => record.turnId === turnId);
    if (!target) {
      return null;
    }

    const replacementParentId = target.parentTurnId;
    const updatedHistory = history
      .filter(record => record.turnId !== turnId)
      .map(record => record.parentTurnId === turnId ? { ...record, parentTurnId: replacementParentId } : record);

    this.turnStore.replaceHistory(updatedHistory);
    this.snapshotStore.removeReferences(target.snapshotIds || []);
    this.onStateChangedEmitter.fire();
    return target;
  }

  public mergeTurnDown(turnId: number): TurnRecord | null {
    const history = this.turnStore.getHistory();
    const sorted = [...history].sort((a, b) => a.turnId - b.turnId);
    const currentIndex = sorted.findIndex(record => record.turnId === turnId);
    if (currentIndex <= 0) {
      return null;
    }

    const newer = sorted[currentIndex];
    const older = sorted[currentIndex - 1];
    const olderBaseline = this.getBaselineForTurn(older, history);
    const mergedFinalState = newer.finalState || older.finalState;
    if (!olderBaseline || !mergedFinalState) {
      return null;
    }

    const mergedChanges = this.diffEngine.compareStatesSync(olderBaseline, mergedFinalState);
    const mergedSnapshotIds = this.collectSnapshotIds(mergedChanges, mergedFinalState);
    const previousSnapshotIds = Array.from(new Set([...(older.snapshotIds || []), ...(newer.snapshotIds || [])]));

    this.snapshotStore.addReferences(mergedSnapshotIds);

    older.changes = mergedChanges;
    older.snapshotIds = mergedSnapshotIds;
    older.summary = this.calculateSummary(mergedChanges);
    older.finalState = mergedFinalState;

    const updatedHistory = history
      .filter(record => record.turnId !== newer.turnId)
      .map(record => record.parentTurnId === newer.turnId ? { ...record, parentTurnId: older.turnId } : record)
      .map(record => record.turnId === older.turnId ? older : record);

    this.snapshotStore.removeReferences(previousSnapshotIds);
    this.turnStore.replaceHistory(updatedHistory);
    this.onStateChangedEmitter.fire();
    return older;
  }

  /**
   * 清空全部历史
   */
  public clearAllHistory(): void {
    const history = this.turnStore.getHistory();
    this.turnStore.clearHistory();
    for (const record of history) {
      this.snapshotStore.removeReferences(record.snapshotIds || []);
    }
    this.onStateChangedEmitter.fire();
  }

  private collectSnapshotIds(changes: TurnRecord['changes'], finalState?: WorkspaceState): string[] {
    const snapshotIds = new Set<string>();
    for (const change of changes) {
      if (change.previousOid) {
        snapshotIds.add(change.previousOid);
      }
      if (change.currentOid) {
        snapshotIds.add(change.currentOid);
      }
    }
    if (finalState) {
      for (const file of Object.values(finalState.files)) {
        if (file.oid) {
          snapshotIds.add(file.oid);
        }
      }
    }
    return Array.from(snapshotIds);
  }

  private collectBaselineSnapshotIds(baseline: TurnBaseline): string[] {
    const snapshotIds = new Set<string>();
    for (const file of Object.values(baseline.files)) {
      if (file.oid) {
        snapshotIds.add(file.oid);
      }
    }
    return Array.from(snapshotIds);
  }

  private cleanupRemovedTurnSnapshots(): void {
    const removedRecord = this.turnStore.consumeLastRemovedTurn();
    if (!removedRecord) {
      return;
    }
    this.snapshotStore.removeReferences(removedRecord.snapshotIds || []);
  }

  private getLatestTurn(history: TurnRecord[]): TurnRecord | undefined {
    if (history.length === 0) {
      return undefined;
    }
    return [...history].sort((a, b) => b.turnId - a.turnId)[0];
  }

  private getLatestTurnId(): number | undefined {
    return this.getLatestTurn(this.turnStore.getHistory())?.turnId;
  }

  private getBaselineForTurn(record: TurnRecord, history: TurnRecord[]): TurnBaseline | null {
    if (!record.parentTurnId) {
      return null;
    }
    const parentTurn = history.find(item => item.turnId === record.parentTurnId);
    if (!parentTurn?.finalState) {
      return null;
    }
    return this.baselineStore.createBaselineFromWorkspaceState(parentTurn.finalState);
  }

  private calculateSummary(changes: TurnRecord['changes']): TurnRecord['summary'] {
    let insertions = 0;
    let deletions = 0;
    for (const change of changes) {
      if (!change.hunks) {
        continue;
      }
      for (const hunk of change.hunks) {
        for (const line of hunk.lines) {
          if (line.startsWith('+')) {
            insertions++;
          } else if (line.startsWith('-')) {
            deletions++;
          }
        }
      }
    }
    return {
      filesChanged: changes.length,
      insertions,
      deletions
    };
  }
}
