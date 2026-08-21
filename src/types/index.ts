export interface TurnSession {
  workspaceRoot: string;
  mode: 'manual' | 'auto';
  nextTurnId: number;
  activeCandidate?: CandidateTurn;
  boundTerminalId?: string;
}

export interface CandidateTurn {
  candidateId: string;
  source: 'manual' | 'command' | 'interactive';
  startedAt: number;
  endedAt?: number;
  state: 'recording' | 'waiting' | 'settling' | 'ready' | 'discarded';
  baseline: TurnBaseline;
  baselineSnapshotIds?: string[];
  terminalContext?: TerminalContext;
}

export interface TurnBaseline {
  baselineId: string;
  createdAt: number;
  files: Record<string, FileBaseline>;
}

export interface FileBaseline {
  exists: boolean;
  oid?: string;
  mode?: string;
  isBinary?: boolean;
}

export interface WorkspaceStateFile {
  exists: boolean;
  oid?: string;
  isBinary?: boolean;
}

export interface WorkspaceState {
  stateId: string;
  createdAt: number;
  files: Record<string, WorkspaceStateFile>;
}

export interface TurnRecord {
  turnId: number;
  source: 'manual' | 'auto';
  createdAt: number;
  terminalContext?: TerminalContext;
  parentTurnId?: number;
  summary: TurnSummary;
  changes: FileChange[];
  finalState?: WorkspaceState;
  snapshotIds?: string[];
}

export interface TurnSummary {
  filesChanged: number;
  insertions: number;
  deletions: number;
  description?: string;
}

export interface FileChange {
  path: string; // 相对路径
  status: 'added' | 'modified' | 'deleted';
  previousOid?: string;
  currentOid?: string;
  hunks?: DiffHunk[];
  inlineChanges?: InlineChange[];
  blockPreviews?: BlockPreview[];
  isBinary?: boolean;
}

export interface DiffHunk {
  oldStart: number;
  oldLines: number;
  newStart: number;
  newLines: number;
  lines: string[]; // 带前缀 ( 如 '+', '-', ' ' )
}

export interface InlineChange {
  line: number;
  startCharacter: number;
  endCharacter: number;
  type: 'added' | 'modified';
}

export interface BlockPreview {
  anchorLine: number;
  removedText?: string;
  addedText?: string;
}

export interface TerminalContext {
  terminalId: string;
  terminalName: string;
  executionKind: 'non_interactive' | 'interactive';
  commandLine?: string;
}
