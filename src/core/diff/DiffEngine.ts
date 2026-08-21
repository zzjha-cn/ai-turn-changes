import { BlockPreview, DiffHunk, FileChange, InlineChange, TurnBaseline, WorkspaceState } from '../../types';
import { GitWorkspaceService } from '../git/GitWorkspaceService';
import { SnapshotStore } from '../storage/SnapshotStore';
import * as fs from 'fs';
import * as path from 'path';
import { diffLines, Change } from 'diff';

export class DiffEngine {
  private gitService: GitWorkspaceService;
  private snapshotStore: SnapshotStore;
  private workspaceRoot: string;

  constructor(gitService: GitWorkspaceService, snapshotStore: SnapshotStore, workspaceRoot: string) {
    this.gitService = gitService;
    this.snapshotStore = snapshotStore;
    this.workspaceRoot = workspaceRoot;
  }

  /**
   * 比较 baseline 与当前工作区状态，生成差异变化
   */
  public async compare(baseline: TurnBaseline): Promise<FileChange[]> {
    const currentState = await this.captureCurrentWorkspaceState();
    return this.compareStates(baseline, currentState);
  }

  public async captureCurrentWorkspaceState(): Promise<WorkspaceState> {
    const files: WorkspaceState['files'] = {};
    const currentCandidates = await this.gitService.listTurnCandidateFiles();

    for (const relPath of currentCandidates) {
      const absolutePath = path.join(this.workspaceRoot, relPath);
      try {
        const isBinary = this.gitService.isBinaryFile(relPath);
        const oid = await this.snapshotStore.storeFileContent(absolutePath);
        files[relPath] = {
          exists: true,
          oid,
          isBinary
        };
      } catch {
        files[relPath] = {
          exists: false
        };
      }
    }

    return {
      stateId: `state_${Date.now()}`,
      createdAt: Date.now(),
      files
    };
  }

  public async compareStates(baseline: TurnBaseline, currentState: WorkspaceState): Promise<FileChange[]> {
    const changes: FileChange[] = [];

    const currentAll = new Set(Object.keys(currentState.files).filter(relPath => currentState.files[relPath].exists));

    const baselineFiles = baseline.files;

    // 2. 对比工作区中每个文件相对于基线的变化
    for (const relPath of currentAll) {
      const baseFile = baselineFiles[relPath];
      const absolutePath = path.join(this.workspaceRoot, relPath);
      const currentFile = currentState.files[relPath];
      const isBinary = currentFile?.isBinary ?? this.gitService.isBinaryFile(relPath);

      if (!baseFile || !baseFile.exists) {
        if (currentFile?.exists && currentFile.oid) {
          const change: FileChange = {
            path: relPath,
            status: 'added',
            currentOid: currentFile.oid,
            isBinary
          };

          if (!isBinary) {
            // 对非二进制新增文件生成完整的 hunk
            const content = fs.readFileSync(absolutePath, 'utf8');
            change.hunks = this.generateAddedHunks(content);
            change.inlineChanges = this.generateAddedInlineChanges(content);
            change.blockPreviews = this.generateAddedBlockPreviews(content);
          }
          changes.push(change);
        }
      } else {
        if (currentFile?.exists && currentFile.oid && currentFile.oid !== baseFile.oid) {
          const change: FileChange = {
            path: relPath,
            status: 'modified',
            previousOid: baseFile.oid,
            currentOid: currentFile.oid,
            isBinary
          };

          if (!isBinary && baseFile.oid) {
            const oldContent = await this.snapshotStore.readContent(baseFile.oid);
            const newContent = fs.readFileSync(absolutePath, 'utf8');
            change.hunks = this.calculateHunks(oldContent, newContent);
            change.inlineChanges = this.calculateInlineChanges(oldContent, newContent, change.hunks);
            change.blockPreviews = this.generateBlockPreviews(change.hunks);
          }
          changes.push(change);
        }
      }
    }

    // 3. 找出基线中存在，但当前工作区中不存在的文件 -> Deleted
    for (const relPath in baselineFiles) {
      const baseFile = baselineFiles[relPath];
      if (baseFile.exists && !currentAll.has(relPath)) {
        changes.push({
          path: relPath,
          status: 'deleted',
          previousOid: baseFile.oid,
          isBinary: baseFile.isBinary
        });
      }
    }

    return changes;
  }

  public compareStatesSync(baseline: TurnBaseline, currentState: WorkspaceState): FileChange[] {
    const changes: FileChange[] = [];
    const currentAll = new Set(Object.keys(currentState.files).filter(relPath => currentState.files[relPath].exists));
    const baselineFiles = baseline.files;

    for (const relPath of currentAll) {
      const baseFile = baselineFiles[relPath];
      const currentFile = currentState.files[relPath];
      const isBinary = currentFile?.isBinary ?? false;

      if (!baseFile || !baseFile.exists) {
        if (currentFile?.exists && currentFile.oid) {
          const change: FileChange = {
            path: relPath,
            status: 'added',
            currentOid: currentFile.oid,
            isBinary
          };
          if (!isBinary) {
            const newContent = this.readSnapshotContentSync(currentFile.oid);
            change.hunks = this.generateAddedHunks(newContent);
            change.inlineChanges = this.generateAddedInlineChanges(newContent);
            change.blockPreviews = this.generateAddedBlockPreviews(newContent);
          }
          changes.push(change);
        }
      } else if (currentFile?.exists && currentFile.oid && currentFile.oid !== baseFile.oid) {
        const change: FileChange = {
          path: relPath,
          status: 'modified',
          previousOid: baseFile.oid,
          currentOid: currentFile.oid,
          isBinary
        };
        if (!isBinary && baseFile.oid) {
          const oldContent = this.readSnapshotContentSync(baseFile.oid);
          const newContent = this.readSnapshotContentSync(currentFile.oid);
          change.hunks = this.calculateHunks(oldContent, newContent);
          change.inlineChanges = this.calculateInlineChanges(oldContent, newContent, change.hunks);
          change.blockPreviews = this.generateBlockPreviews(change.hunks);
        }
        changes.push(change);
      }
    }

    for (const relPath in baselineFiles) {
      const baseFile = baselineFiles[relPath];
      if (baseFile.exists && !currentAll.has(relPath)) {
        changes.push({
          path: relPath,
          status: 'deleted',
          previousOid: baseFile.oid,
          isBinary: baseFile.isBinary
        });
      }
    }

    return changes.sort((a, b) => a.path.localeCompare(b.path));
  }

  private readSnapshotContentSync(snapshotId: string): string {
    return this.snapshotStore.readContentSync(snapshotId);
  }

  /**
   * 为纯新增文件生成首个 hunk
   */
  private generateAddedHunks(content: string): DiffHunk[] {
    const lines = content.split(/\r?\n/);
    return [{
      oldStart: 0,
      oldLines: 0,
      newStart: 1,
      newLines: lines.length,
      lines: lines.map(l => '+' + l)
    }];
  }

  private generateAddedInlineChanges(content: string): InlineChange[] {
    const lines = content.split(/\r?\n/);
    return lines.map((line, index) => ({
      line: index,
      startCharacter: 0,
      endCharacter: Math.max(line.length, 1),
      type: 'added' as const
    }));
  }

  private generateAddedBlockPreviews(content: string): BlockPreview[] {
    return [{
      anchorLine: 0,
      addedText: content
    }];
  }

  private generateBlockPreviews(hunks: DiffHunk[]): BlockPreview[] {
    return hunks.map(hunk => {
      const removedLines = hunk.lines.filter(line => line.startsWith('-')).map(line => line.slice(1));
      const addedLines = hunk.lines.filter(line => line.startsWith('+')).map(line => line.slice(1));
      return {
        anchorLine: Math.max(hunk.newStart - 1, 0),
        removedText: removedLines.length > 0 ? removedLines.join('\n') : undefined,
        addedText: addedLines.length > 0 ? addedLines.join('\n') : undefined
      };
    }).filter(block => block.removedText || block.addedText);
  }

  /**
   * 采用 jsdiff 算法，计算旧内容与新内容之间的 hunk 块
   */
  private calculateHunks(oldContent: string, newContent: string): DiffHunk[] {
    const diffChanges: Change[] = diffLines(oldContent, newContent);
    const hunks: DiffHunk[] = [];

    let oldLineCounter = 1;
    let newLineCounter = 1;

    // 为简化结构，将 diffChanges 的连续块聚合成统一的 DiffHunk 列表
    let currentHunkLines: string[] = [];
    let hunkOldStart = 0;
    let hunkOldLines = 0;
    let hunkNewStart = 0;
    let hunkNewLines = 0;

    const flushHunk = () => {
      if (currentHunkLines.length > 0) {
        hunks.push({
          oldStart: hunkOldStart,
          oldLines: hunkOldLines,
          newStart: hunkNewStart,
          newLines: hunkNewLines,
          lines: [...currentHunkLines]
        });
        currentHunkLines = [];
        hunkOldLines = 0;
        hunkNewLines = 0;
      }
    };

    for (const change of diffChanges) {
      const count = change.count || 0;
      const lines = change.value.split(/\r?\n/);
      if (lines.length > 1 && lines[lines.length - 1] === '') {
        lines.pop(); // 过滤最后的空行
      }

      if (change.added) {
        if (currentHunkLines.length === 0) {
          hunkOldStart = oldLineCounter;
          hunkNewStart = newLineCounter;
        }
        hunkNewLines += count;
        currentHunkLines.push(...lines.map((l: string) => '+' + l));
        newLineCounter += count;
      } else if (change.removed) {
        if (currentHunkLines.length === 0) {
          hunkOldStart = oldLineCounter;
          hunkNewStart = newLineCounter;
        }
        hunkOldLines += count;
        currentHunkLines.push(...lines.map((l: string) => '-' + l));
        oldLineCounter += count;
      } else {
        // 未变行，作为上下文。如果是连续变更，可用于 flush 上一个 hunk
        flushHunk();
        oldLineCounter += count;
        newLineCounter += count;
      }
    }
    flushHunk();

    return hunks;
  }

  private calculateInlineChanges(oldContent: string, newContent: string, hunks: DiffHunk[]): InlineChange[] {
    const inlineChanges: InlineChange[] = [];
    const oldLines = oldContent.split(/\r?\n/);
    const newLines = newContent.split(/\r?\n/);

    for (const hunk of hunks) {
      const removedLines = hunk.lines.filter(line => line.startsWith('-')).map(line => line.slice(1));
      const addedLines = hunk.lines.filter(line => line.startsWith('+')).map(line => line.slice(1));
      const pairCount = Math.min(removedLines.length, addedLines.length);

      for (let i = 0; i < pairCount; i++) {
        const newLineIndex = hunk.newStart - 1 + i;
        const oldLine = removedLines[i] ?? oldLines[hunk.oldStart - 1 + i] ?? '';
        const newLine = addedLines[i] ?? newLines[newLineIndex] ?? '';
        const range = this.findChangedSpan(oldLine, newLine);
        inlineChanges.push({
          line: newLineIndex,
          startCharacter: range.start,
          endCharacter: range.end,
          type: 'modified'
        });
      }

      if (addedLines.length > removedLines.length) {
        for (let i = pairCount; i < addedLines.length; i++) {
          const newLineIndex = hunk.newStart - 1 + i;
          const line = addedLines[i] ?? newLines[newLineIndex] ?? '';
          inlineChanges.push({
            line: newLineIndex,
            startCharacter: 0,
            endCharacter: Math.max(line.length, 1),
            type: 'added'
          });
        }
      }
    }

    return inlineChanges.filter(change => change.line >= 0);
  }

  private findChangedSpan(oldLine: string, newLine: string): { start: number; end: number } {
    let start = 0;
    const maxPrefix = Math.min(oldLine.length, newLine.length);
    while (start < maxPrefix && oldLine[start] === newLine[start]) {
      start++;
    }

    let oldEnd = oldLine.length - 1;
    let newEnd = newLine.length - 1;
    while (oldEnd >= start && newEnd >= start && oldLine[oldEnd] === newLine[newEnd]) {
      oldEnd--;
      newEnd--;
    }

    return {
      start,
      end: Math.max(start + 1, newEnd + 1)
    };
  }
}
