import { FileBaseline, TurnBaseline, WorkspaceState } from '../../types';
import { GitWorkspaceService } from '../git/GitWorkspaceService';
import { SnapshotStore } from '../storage/SnapshotStore';
import * as crypto from 'crypto';

export class BaselineStore {
  private gitService: GitWorkspaceService;
  private snapshotStore: SnapshotStore;

  constructor(gitService: GitWorkspaceService, snapshotStore: SnapshotStore) {
    this.gitService = gitService;
    this.snapshotStore = snapshotStore;
  }

  /**
   * 创建当前工作区的文件基线
   */
  public async createBaseline(): Promise<TurnBaseline> {
    const baselineId = 'baseline_' + crypto.randomBytes(8).toString('hex');
    const files: Record<string, FileBaseline> = {};

    const tracked = await this.gitService.getTrackedFiles();
    const candidates = await this.gitService.listTurnCandidateFiles();

    // 1. 依次记录已跟踪文件的状态和 OID
    for (const relPath of tracked) {
      try {
        const isBinary = this.gitService.isBinaryFile(relPath);
        const oid = await this.snapshotStore.storeFileContent(this.gitService.resolveAbsolutePath(relPath));
        files[relPath] = {
          exists: true,
          oid,
          isBinary
        };
      } catch (e) {
        // 如果文件虽然被跟踪但突然被删除，记为不存在
        files[relPath] = { exists: false };
      }
    }

    // 2. 对于遵循 gitignore 后仍可见的未跟踪文件，基线标记为不存在，用于识别新增
    for (const relPath of candidates) {
      if (!files[relPath]) {
        files[relPath] = { exists: false };
      }
    }

    return {
      baselineId,
      createdAt: Date.now(),
      files
    };
  }

  public async captureWorkspaceState(): Promise<WorkspaceState> {
    const baseline = await this.createBaseline();
    return {
      stateId: baseline.baselineId,
      createdAt: baseline.createdAt,
      files: baseline.files
    };
  }

  public createBaselineFromWorkspaceState(workspaceState: WorkspaceState): TurnBaseline {
    return {
      baselineId: workspaceState.stateId,
      createdAt: workspaceState.createdAt,
      files: Object.fromEntries(
        Object.entries(workspaceState.files).map(([key, value]) => [key, { ...value } as FileBaseline])
      )
    };
  }
}
