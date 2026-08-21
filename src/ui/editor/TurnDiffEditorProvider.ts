import * as vscode from 'vscode';
import { DiffHunk, FileChange, TurnRecord } from '../../types';
import { SnapshotStore } from '../../core/storage/SnapshotStore';
import * as path from 'path';

type DiffDocumentState = {
  turnId: number;
  filePath: string;
};

type HighlightRange = {
  start: number;
  end: number;
};

type AugmentedInjectedBlock = {
  type: 'old' | 'new';
  summary: string;
  lines: Array<{
    lineNumber?: number;
    text: string;
    emphasis?: HighlightRange[];
  }>;
};


type AugmentedBlockPair = {
  anchorLine: number;
  summary: string;
  oldBlock?: AugmentedInjectedBlock;
  newBlock?: AugmentedInjectedBlock;
};
type AugmentedSourceLine = {
  lineNumber?: number;
  text: string;
  kind: 'normal' | 'deleted-placeholder' | 'paired-block-anchor';
  emphasis?: HighlightRange[];
  pair?: AugmentedBlockPair;
};

type AugmentedSourcePayload = {
  turnId: number;
  filePath: string;
  status: FileChange['status'];
  totalLines: number;
  lines: AugmentedSourceLine[];
};

export class TurnDiffEditorProvider implements vscode.CustomTextEditorProvider, vscode.TextDocumentContentProvider {
  public static readonly viewType = 'turnChanges.diffEditor';
  public static readonly scheme = 'turn-diff';

  private readonly snapshotStore: SnapshotStore;
  private readonly getTurnById: (turnId: number) => TurnRecord | undefined;
  private readonly openSourceFile: (record: TurnRecord, change: FileChange, lineNumber?: number) => Promise<void>;
  private readonly getDefaultTheme: () => 'dark' | 'light';
  private readonly workspaceRoot: string;
  private readonly panels = new Map<string, vscode.WebviewPanel>();
  private readonly refreshTimers = new Map<string, NodeJS.Timeout>();

  constructor(
    snapshotStore: SnapshotStore,
    getTurnById: (turnId: number) => TurnRecord | undefined,
    openSourceFile: (record: TurnRecord, change: FileChange, lineNumber?: number) => Promise<void>,
    getDefaultTheme: () => 'dark' | 'light',
    workspaceRoot: string
  ) {
    this.snapshotStore = snapshotStore;
    this.getTurnById = getTurnById;
    this.openSourceFile = openSourceFile;
    this.getDefaultTheme = getDefaultTheme;
    this.workspaceRoot = workspaceRoot;
  }

  public static createUri(turnId: number, filePath: string): vscode.Uri {
    const encodedPath = encodeURIComponent(filePath);
    return vscode.Uri.parse(`${TurnDiffEditorProvider.scheme}:/${encodedPath}.turn-diff?turnId=${turnId}&path=${encodedPath}`);
  }

  public provideTextDocumentContent(uri: vscode.Uri): string {
    const state = this.parseDocumentState(uri);
    return [`Turn Diff`, `Turn: ${state.turnId}`, `Path: ${state.filePath}`].join('\n');
  }

  public async openDiffForChange(record: TurnRecord, change: FileChange): Promise<void> {
    const uri = TurnDiffEditorProvider.createUri(record.turnId, change.path);
    await vscode.commands.executeCommand('vscode.openWith', uri, TurnDiffEditorProvider.viewType, {
      preview: false
    });
  }

  public async resolveCustomTextEditor(document: vscode.TextDocument, webviewPanel: vscode.WebviewPanel): Promise<void> {
    webviewPanel.webview.options = {
      enableScripts: true
    };

    const state = this.parseDocumentState(document.uri);
    const panelKey = this.getPanelKey(state.turnId, state.filePath);
    this.panels.set(panelKey, webviewPanel);
    webviewPanel.onDidDispose(() => {
      this.panels.delete(panelKey);
      const timer = this.refreshTimers.get(panelKey);
      if (timer) {
        clearTimeout(timer);
        this.refreshTimers.delete(panelKey);
      }
    });

    await this.renderPanel(webviewPanel, state);

    webviewPanel.webview.onDidReceiveMessage(async message => {
      const latestRecord = this.getTurnById(state.turnId);
      const latestChange = latestRecord?.changes.find(item => item.path === state.filePath);
      if (!latestRecord || !latestChange) {
        return;
      }

      if (message?.type === 'openSource') {
        await this.openSourceFile(latestRecord, latestChange, message.lineNumber);
      }
      if (message?.type === 'openSourceSideBySide') {
        await this.openSourceSideBySide(latestRecord, latestChange, message.lineNumber);
      }
    });
  }

  public async refreshForWorkspaceFile(relativePath: string): Promise<void> {
    const normalized = relativePath.replace(/\\/g, '/');
    for (const [key, panel] of this.panels.entries()) {
      const state = this.parsePanelKey(key);
      if (state.filePath !== normalized) {
        continue;
      }
      this.schedulePanelRefresh(key, panel, state);
    }
  }

  private parseDocumentState(uri: vscode.Uri): DiffDocumentState {
    const params = new URLSearchParams(uri.query);
    const turnId = Number(params.get('turnId') || '0');
    const filePath = decodeURIComponent(params.get('path') || '');
    return { turnId, filePath };
  }

  private getPanelKey(turnId: number, filePath: string): string {
    return `${turnId}:${filePath}`;
  }

  private parsePanelKey(key: string): DiffDocumentState {
    const separator = key.indexOf(':');
    const turnId = Number(key.slice(0, separator));
    const filePath = key.slice(separator + 1);
    return { turnId, filePath };
  }

  private schedulePanelRefresh(key: string, panel: vscode.WebviewPanel, state: DiffDocumentState): void {
    const existing = this.refreshTimers.get(key);
    if (existing) {
      clearTimeout(existing);
    }

    const timer = setTimeout(() => {
      this.refreshTimers.delete(key);
      void this.refreshPanelFromWorkspace(panel, state);
    }, 120);

    this.refreshTimers.set(key, timer);
  }

  private async refreshPanelFromWorkspace(webviewPanel: vscode.WebviewPanel, state: DiffDocumentState): Promise<void> {
    const record = this.getTurnById(state.turnId);
    const change = record?.changes.find(item => item.path === state.filePath);

    if (!record || !change || change.status === 'deleted') {
      await this.renderPanel(webviewPanel, state);
      return;
    }

    try {
      const currentChange = await this.buildWorkspaceLinkedChange(record, change);
      const payload = await this.buildPayload(record, currentChange);
      webviewPanel.webview.html = this.renderHtml(payload);
    } catch {
      await this.renderPanel(webviewPanel, state);
    }
  }

  private async buildWorkspaceLinkedChange(record: TurnRecord, change: FileChange): Promise<FileChange> {
    const absolutePath = path.join(this.workspaceRoot, change.path);
    const fileUri = vscode.Uri.file(absolutePath);
    const document = await vscode.workspace.openTextDocument(fileUri);
    const liveContent = document.getText();
    const liveOid = await this.snapshotStore.storeTextContent(liveContent);

    return {
      ...change,
      previousOid: this.resolveRefreshPreviousOid(record, change),
      currentOid: liveOid
    };
  }

  private resolveRefreshPreviousOid(record: TurnRecord, change: FileChange): string | undefined {
    if (change.status === 'added') {
      return undefined;
    }

    if (change.previousOid) {
      return change.previousOid;
    }

    return record.finalState?.files[change.path]?.oid;
  }

  private async renderPanel(webviewPanel: vscode.WebviewPanel, state: DiffDocumentState): Promise<void> {
    const record = this.getTurnById(state.turnId);
    const change = record?.changes.find(item => item.path === state.filePath);

    if (!record || !change) {
      webviewPanel.webview.html = this.renderMissingHtml();
      return;
    }

    const payload = await this.buildPayload(record, change);
    webviewPanel.webview.html = this.renderHtml(payload);
  }

  private async openSourceSideBySide(_record: TurnRecord, change: FileChange, lineNumber?: number): Promise<void> {
    const absolutePath = path.join(this.workspaceRoot, change.path);
    const fileUri = vscode.Uri.file(absolutePath);
    const doc = await vscode.workspace.openTextDocument(fileUri);
    const editor = await vscode.window.showTextDocument(doc, {
      preview: false,
      viewColumn: vscode.ViewColumn.Beside,
      preserveFocus: true
    });

    if (lineNumber && lineNumber > 0) {
      const targetLine = Math.min(Math.max(lineNumber - 1, 0), Math.max(doc.lineCount - 1, 0));
      const position = new vscode.Position(targetLine, 0);
      const range = new vscode.Range(position, position);
      editor.selection = new vscode.Selection(position, position);
      editor.revealRange(range, vscode.TextEditorRevealType.InCenter);
    }
  }

  private async buildPayload(record: TurnRecord, change: FileChange): Promise<AugmentedSourcePayload> {
    const oldContent = change.previousOid ? await this.snapshotStore.readContent(change.previousOid) : '';
    const newContent = change.currentOid ? await this.snapshotStore.readContent(change.currentOid) : '';
    const lines = this.buildAugmentedLines(change, oldContent, newContent);

    return {
      turnId: record.turnId,
      filePath: change.path,
      status: change.status,
      totalLines: lines.length,
      lines
    };
  }

  private buildAugmentedLines(change: FileChange, oldContent: string, newContent: string): AugmentedSourceLine[] {
    const newLines = newContent.length > 0 ? newContent.split(/\r?\n/) : [];
    const oldLines = oldContent.length > 0 ? oldContent.split(/\r?\n/) : [];
    const sourceLines: AugmentedSourceLine[] = [];

    const inlineHighlightMap = new Map<number, HighlightRange[]>();
    for (const inlineChange of change.inlineChanges || []) {
      const existing = inlineHighlightMap.get(inlineChange.line) || [];
      existing.push({ start: inlineChange.startCharacter, end: inlineChange.endCharacter });
      inlineHighlightMap.set(inlineChange.line, existing);
    }

    const hunkMap = new Map<number, DiffHunk[]>();
    for (const hunk of change.hunks || []) {
      const anchorIndex = this.resolveAnchorIndex(change, hunk, newLines.length);
      const existing = hunkMap.get(anchorIndex) || [];
      existing.push(hunk);
      hunkMap.set(anchorIndex, existing);
    }

    if (change.status === 'deleted') {
      const deletedBlock = this.buildDeletedOnlyBlock(oldLines);
      return [{
        lineNumber: undefined,
        text: '',
        kind: 'deleted-placeholder',
        pair: {
          anchorLine: 0,
          summary: 'Deleted file',
          oldBlock: deletedBlock
        }
      }];
    }

    for (let index = 0; index < newLines.length; index++) {
      const pair = this.buildPair(hunkMap.get(index) || [], inlineHighlightMap, index);
      const isChangedAnchor = Boolean(pair);

      sourceLines.push({
        lineNumber: index + 1,
        text: newLines[index] ?? '',
        kind: isChangedAnchor ? 'paired-block-anchor' : 'normal',
        emphasis: isChangedAnchor ? undefined : inlineHighlightMap.get(index),
        pair
      });
    }

    const tailBlocks = hunkMap.get(newLines.length) || [];
    if (tailBlocks.length > 0) {
      sourceLines.push({
        lineNumber: undefined,
        text: '',
        kind: 'deleted-placeholder',
        pair: this.buildPair(tailBlocks, inlineHighlightMap, newLines.length)
      });
    }

    if (sourceLines.length === 0) {
      sourceLines.push({
        lineNumber: 1,
        text: '',
        kind: 'normal'
      });
    }

    return sourceLines;
  }

  private buildDeletedOnlyBlock(oldLines: string[]): AugmentedInjectedBlock {
    return {
      type: 'old',
      summary: `${oldLines.length} original lines`,
      lines: oldLines.map((text, index) => ({
        lineNumber: index + 1,
        text,
        emphasis: [{ start: 0, end: Math.max(text.length, 1) }]
      }))
    };
  }

  private buildPair(hunks: DiffHunk[], inlineHighlightMap: Map<number, HighlightRange[]>, anchorIndex: number): AugmentedBlockPair | undefined {
    if (hunks.length === 0) {
      return undefined;
    }

    const hunk = hunks[0];
    const oldBlock = this.buildInjectedOldBlock(hunk);
    const newBlock = this.buildInjectedNewBlock(hunk, inlineHighlightMap) || undefined;
    return {
      anchorLine: anchorIndex + 1,
      summary: this.buildBlockSummary(oldBlock.lines.length, newBlock?.lines.length || 0),
      oldBlock: oldBlock.lines.length > 0 ? oldBlock : undefined,
      newBlock
    };
  }

  private buildInjectedOldBlock(hunk: DiffHunk): AugmentedInjectedBlock {
    const removedLines = hunk.lines.filter(line => line.startsWith('-')).map(line => line.slice(1));
    const addedLines = hunk.lines.filter(line => line.startsWith('+')).map(line => line.slice(1));
    const pairedRanges = this.buildPairedRanges(removedLines, addedLines);

    return {
      type: 'old',
      summary: this.buildBlockSummary(removedLines.length, addedLines.length),
      lines: removedLines.map((text, index) => ({
        lineNumber: hunk.oldStart + index,
        text,
        emphasis: pairedRanges[index]?.old || [{ start: 0, end: Math.max(text.length, 1) }]
      }))
    };
  }

  private buildInjectedNewBlock(
    hunk: DiffHunk,
    inlineHighlightMap: Map<number, HighlightRange[]>
  ): AugmentedInjectedBlock | null {
    const addedLines = hunk.lines.filter(line => line.startsWith('+')).map(line => line.slice(1));
    const removedLines = hunk.lines.filter(line => line.startsWith('-')).map(line => line.slice(1));
    if (addedLines.length === 0) {
      return null;
    }

    const pairedRanges = this.buildPairedRanges(removedLines, addedLines);
    return {
      type: 'new',
      summary: this.buildBlockSummary(removedLines.length, addedLines.length),
      lines: addedLines.map((text, index) => ({
        lineNumber: hunk.newStart + index,
        text,
        emphasis: pairedRanges[index]?.new || inlineHighlightMap.get(hunk.newStart - 1 + index) || [{ start: 0, end: Math.max(text.length, 1) }]
      }))
    };
  }

  private buildBlockSummary(removedCount: number, addedCount: number): string {
    if (removedCount > 0 && addedCount > 0) {
      return `${removedCount} → ${addedCount}`;
    }
    if (removedCount > 0) {
      return `-${removedCount}`;
    }
    return `+${addedCount}`;
  }

  private buildPairedRanges(removedLines: string[], addedLines: string[]): Array<{ old?: HighlightRange[]; new?: HighlightRange[] }> {
    const pairCount = Math.min(removedLines.length, addedLines.length);
    const pairs: Array<{ old?: HighlightRange[]; new?: HighlightRange[] }> = [];

    for (let index = 0; index < pairCount; index++) {
      const oldText = removedLines[index] || '';
      const newText = addedLines[index] || '';
      const range = this.findChangedSpan(oldText, newText);
      pairs[index] = {
        old: [{ start: range.oldStart, end: range.oldEnd }],
        new: [{ start: range.newStart, end: range.newEnd }]
      };
    }

    return pairs;
  }

  private findChangedSpan(oldLine: string, newLine: string): { oldStart: number; oldEnd: number; newStart: number; newEnd: number } {
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
      oldStart: start,
      oldEnd: Math.max(start + 1, oldEnd + 1),
      newStart: start,
      newEnd: Math.max(start + 1, newEnd + 1)
    };
  }

  private resolveAnchorIndex(change: FileChange, hunk: DiffHunk, newLineCount: number): number {
    if (change.status === 'added') {
      return Math.max(hunk.newStart - 1, 0);
    }
    if (hunk.newLines === 0) {
      return Math.min(Math.max(hunk.newStart - 1, 0), newLineCount);
    }
    return Math.min(Math.max(hunk.newStart - 1, 0), Math.max(newLineCount - 1, 0));
  }

  private renderHtml(payload: AugmentedSourcePayload): string {
    const escaped = JSON.stringify(payload).replace(/</g, '\\u003c');
    return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <style>
    :root {
      --bg: #0f1117;
      --panel: #151922;
      --border: rgba(255,255,255,0.08);
      --text: #e6edf3;
      --muted: #98a2b3;
      --line-number: #687385;
      --deleted-ghost: rgba(248, 81, 73, 0.04);
      --old-bg: rgba(248, 81, 73, 0.18);
      --old-border: rgba(248, 81, 73, 0.46);
      --new-bg: rgba(63, 185, 80, 0.16);
      --new-border: rgba(63, 185, 80, 0.44);
      --inline-old: rgba(248, 81, 73, 0.34);
      --inline-new: rgba(63, 185, 80, 0.34);
      --toolbar-button-bg: rgba(255,255,255,0.03);
      --toolbar-button-border: rgba(255,255,255,0.08);
      --toolbar-button-text: var(--text);
    }
    body[data-theme="light"] {
      --bg: #ffffff;
      --panel: #f7f8fa;
      --border: rgba(15, 23, 42, 0.10);
      --text: #0f172a;
      --muted: #64748b;
      --line-number: #94a3b8;
      --deleted-ghost: rgba(239, 68, 68, 0.06);
      --old-bg: rgba(239, 68, 68, 0.16);
      --old-border: rgba(220, 38, 38, 0.34);
      --new-bg: rgba(34, 197, 94, 0.16);
      --new-border: rgba(22, 163, 74, 0.34);
      --inline-old: rgba(239, 68, 68, 0.24);
      --inline-new: rgba(34, 197, 94, 0.24);
      --toolbar-button-bg: #ffffff;
      --toolbar-button-border: rgba(15, 23, 42, 0.12);
      --toolbar-button-text: #0f172a;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      background: var(--bg);
      color: var(--text);
      font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
    }
    .page {
      padding: 18px;
      display: flex;
      flex-direction: column;
      gap: 14px;
    }
    .header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      gap: 16px;
      padding: 14px 16px;
      border: 1px solid var(--border);
      border-radius: 14px;
      background: var(--panel);
    }
    .title { display: flex; flex-direction: column; gap: 6px; }
    .eyebrow { color: var(--muted); font-size: 12px; }
    .path { font-size: 15px; font-weight: 600; word-break: break-all; }
    .meta { color: var(--muted); font-size: 12px; }
    .action {
      border: 1px solid var(--toolbar-button-border);
      background: var(--toolbar-button-bg);
      color: var(--toolbar-button-text);
      border-radius: 10px;
      padding: 8px 12px;
      cursor: pointer;
    }
    .actions {
      display: flex;
      align-items: center;
      gap: 8px;
    }
    .editor-shell {
      border: 1px solid var(--border);
      border-radius: 16px;
      overflow: hidden;
      background: var(--bg);
      box-shadow: 0 12px 36px rgba(0,0,0,0.20);
    }
    .editor-toolbar {
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding: 10px 14px;
      background: var(--panel);
      border-bottom: 1px solid var(--border);
      color: var(--muted);
      font-size: 12px;
    }
    .editor {
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      font-size: 12px;
      line-height: 1.7;
    }
    .row {
      display: grid;
      grid-template-columns: 64px minmax(0, 1fr);
      align-items: stretch;
      border-left: 3px solid transparent;
    }
    .row.normal { background: transparent; }
    .row.deleted-placeholder { background: var(--deleted-ghost); border-left-color: rgba(248, 81, 73, 0.5); }
    .row.paired-block-anchor { background: rgba(255,255,255,0.01); border-left-color: rgba(255,255,255,0.06); }
    .gutter {
      padding: 0 12px 0 0;
      text-align: right;
      color: var(--line-number);
      user-select: none;
      border-right: 1px solid rgba(255,255,255,0.04);
    }
    .cell {
      padding: 0 16px;
      min-height: 24px;
      white-space: pre-wrap;
      word-break: break-word;
      opacity: 0.72;
    }
    .row:hover { background: rgba(255,255,255,0.03); }
    .row:hover .cell { opacity: 0.96; }
    .line-clickable { cursor: pointer; }
    .row.is-active { background: rgba(88, 166, 255, 0.08); border-left-color: rgba(88, 166, 255, 0.55); }
    .row.is-active .cell { opacity: 1; }
    .inline-new { background: var(--inline-new); border-radius: 4px; }
    .inline-old { background: var(--inline-old); border-radius: 4px; }
    .injected {
      display: flex;
      flex-direction: column;
      gap: 0;
      padding: 6px 0;
    }
    .pair-wrapper {
      position: relative;
      border-radius: 14px;
      overflow: hidden;
      border: 1px solid rgba(255,255,255,0.05);
      background: rgba(255,255,255,0.01);
      box-shadow: 0 16px 36px rgba(0,0,0,0.18);
    }
    .pair-jump {
      position: absolute;
      right: 12px;
      bottom: 12px;
      width: 28px;
      height: 28px;
      display: inline-flex;
      align-items: center;
      justify-content: center;
      border-radius: 999px;
      border: 1px solid rgba(96,165,250,0.38);
      background: rgba(37,99,235,0.34);
      color: rgba(239,246,255,0.98);
      cursor: pointer;
      font-size: 14px;
      line-height: 1;
      box-shadow: 0 10px 24px rgba(37,99,235,0.22);
      transition: background 120ms ease, border-color 120ms ease, transform 120ms ease, box-shadow 120ms ease;
    }
    .pair-jump:hover {
      background: rgba(59,130,246,0.52);
      border-color: rgba(147,197,253,0.72);
      box-shadow: 0 14px 30px rgba(59,130,246,0.3);
      transform: translateY(-1px);
    }
    body[data-theme="light"] .pair-jump {
      background: rgba(37,99,235,0.14);
      color: rgba(29,78,216,0.96);
      border-color: rgba(37,99,235,0.22);
      box-shadow: 0 10px 24px rgba(37,99,235,0.12);
    }
    .pair-connector {
      height: 10px;
      background: linear-gradient(180deg, rgba(248,81,73,0.10) 0%, rgba(63,185,80,0.10) 100%);
      border-top: 1px solid rgba(255,255,255,0.04);
      border-bottom: 1px solid rgba(255,255,255,0.04);
    }
    .pair-summary {
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding: 6px 10px;
      font-size: 11px;
      color: rgba(152,162,179,0.72);
      background: rgba(255,255,255,0.015);
      border-bottom: 1px solid rgba(255,255,255,0.04);
    }
    body[data-theme="light"] .pair-summary {
      color: rgba(100,116,139,0.88);
      background: rgba(15,23,42,0.02);
      border-bottom-color: rgba(15,23,42,0.06);
    }
    .old-block {
      position: relative;
      border: 0;
      background: var(--old-bg);
      border-radius: 0;
      overflow: hidden;
      box-shadow: none;
    }
    .new-block {
      position: relative;
      border: 0;
      background: var(--new-bg);
      border-radius: 0;
      overflow: hidden;
      box-shadow: none;
    }
    .block-corner-label {
      position: absolute;
      right: 10px;
      font-size: 10px;
      letter-spacing: 0.02em;
      text-transform: lowercase;
      opacity: 0.7;
      pointer-events: none;
    }
    .old-corner-label {
      top: 6px;
      color: rgba(255, 201, 197, 0.78);
    }
    .new-corner-label {
      bottom: 6px;
      color: rgba(183, 240, 193, 0.78);
    }
    body[data-theme="light"] .old-corner-label {
      color: rgba(185, 28, 28, 0.78);
    }
    body[data-theme="light"] .new-corner-label {
      color: rgba(21, 128, 61, 0.78);
    }
    .old-line {
      display: grid;
      grid-template-columns: 64px minmax(0, 1fr);
      padding: 0;
    }
    .new-line {
      display: grid;
      grid-template-columns: 64px minmax(0, 1fr);
      padding: 0;
    }
    .old-line-number {
      padding: 0 12px 0 0;
      text-align: right;
      color: rgba(255, 180, 174, 0.66);
      user-select: none;
      border-right: 1px solid rgba(248,81,73,0.14);
    }
    .new-line-number {
      padding: 0 12px 0 0;
      text-align: right;
      color: rgba(141, 219, 155, 0.72);
      user-select: none;
      border-right: 1px solid rgba(63,185,80,0.14);
    }
    .old-line-text {
      padding: 0 36px 0 16px;
      min-height: 24px;
      white-space: pre-wrap;
      word-break: break-word;
    }
    .new-line-text {
      padding: 0 36px 0 16px;
      min-height: 24px;
      white-space: pre-wrap;
      word-break: break-word;
    }
    .empty {
      padding: 24px;
      color: var(--muted);
      text-align: center;
    }
  </style>
</head>
<body data-theme="${this.getDefaultTheme()}">
  <div class="page" id="app"></div>
  <script>
    const vscode = acquireVsCodeApi();
    const payload = ${escaped};
    const state = vscode.getState() || { theme: '${this.getDefaultTheme()}' };

    function escapeHtml(text) {
      return String(text ?? '')
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#39;');
    }

    function renderWithEmphasis(text, emphasis, className) {
      const value = text ?? '';
      if (!emphasis || emphasis.length === 0) {
        return escapeHtml(value || ' ');
      }
      let cursor = 0;
      let html = '';
      const sorted = [...emphasis].sort((a, b) => a.start - b.start);
      for (const item of sorted) {
        const start = Math.max(0, Math.min(item.start, value.length));
        const end = Math.max(start, Math.min(item.end, value.length));
        html += escapeHtml(value.slice(cursor, start));
        html += '<span class="' + className + '">' + escapeHtml(value.slice(start, end) || ' ') + '</span>';
        cursor = end;
      }
      html += escapeHtml(value.slice(cursor));
      return html || '&nbsp;';
    }

    function renderOldBlock(block) {
      const lines = block.lines.map(line => {
        const lineNumber = line.lineNumber ?? '';
        return '<div class="old-line">' +
          '<div class="old-line-number">' + lineNumber + '</div>' +
          '<div class="old-line-text">' + renderWithEmphasis(line.text, line.emphasis, 'inline-old') + '</div>' +
        '</div>';
      }).join('');
      return '<div class="old-block">' +
        '<div class="block-corner-label old-corner-label">old</div>' +
        lines +
      '</div>';
    }

    function renderNewBlock(block) {
      const lines = block.lines.map(line => {
        const lineNumber = line.lineNumber ?? '';
        return '<div class="new-line">' +
          '<div class="new-line-number">' + lineNumber + '</div>' +
          '<div class="new-line-text">' + renderWithEmphasis(line.text, line.emphasis, 'inline-new') + '</div>' +
        '</div>';
      }).join('');
      return '<div class="new-block">' +
        '<div class="block-corner-label new-corner-label">new</div>' +
        lines +
      '</div>';
    }

    function renderPair(pair) {
      if (!pair) {
        return '';
      }
      const oldHtml = pair.oldBlock ? renderOldBlock(pair.oldBlock) : '';
      const newHtml = pair.newBlock ? renderNewBlock(pair.newBlock) : '';
      const connector = pair.oldBlock && pair.newBlock ? '<div class="pair-connector"></div>' : '';
      const jumpButton = '<button class="pair-jump" data-anchor-line="' + pair.anchorLine + '" title="Next change">↓</button>';
      return '<div class="injected"><div class="pair-wrapper">' +
        jumpButton +
        '<div class="pair-summary"><span>Paired change · Line ' + pair.anchorLine + '</span><span>' + pair.summary + '</span></div>' +
        oldHtml + connector + newHtml +
      '</div></div>';
    }

    function renderRow(line) {
      const lineNumber = line.lineNumber ?? '';
      const text = renderWithEmphasis(line.text, line.emphasis, line.kind === 'modified' ? 'inline-new' : 'inline-new');
      const row = '<div class="row ' + line.kind + '" data-line="' + (line.lineNumber || '') + '">' +
        '<div class="gutter">' + lineNumber + '</div>' +
        '<div class="cell">' + (line.text ? text : '&nbsp;') + '</div>' +
      '</div>';
      if (line.kind === 'paired-block-anchor' && line.pair) {
        return row + renderPair(line.pair);
      }
      return row;
    }

    function activateChange(changeRows, nextIndex) {
      if (changeRows.length === 0) {
        return;
      }
      const normalizedIndex = ((nextIndex % changeRows.length) + changeRows.length) % changeRows.length;
      const nextRow = changeRows[normalizedIndex];
      document.querySelectorAll('.row.is-active').forEach(active => active.classList.remove('is-active'));
      nextRow.classList.add('is-active');
      nextRow.scrollIntoView({ block: 'center', behavior: 'smooth' });
      state.changeIndex = normalizedIndex;
      vscode.setState(state);
    }

    function render() {
      const app = document.getElementById('app');
      document.body.setAttribute('data-theme', state.theme || 'dark');
      if (!payload.lines || payload.lines.length === 0) {
        app.innerHTML = '<div class="header"><div class="title"><div class="eyebrow">Turn #' + payload.turnId + '</div><div class="path">' + escapeHtml(payload.filePath) + '</div></div></div><div class="empty">No diff content available for this file.</div>';
        return;
      }

      const header = '<div class="header">' +
        '<div class="title">' +
          '<div class="eyebrow">Turn #' + payload.turnId + ' · Augmented Source View</div>' +
          '<div class="path">' + escapeHtml(payload.filePath) + '</div>' +
          '<div class="meta">' + payload.status + ' · Full source with inline paired blocks</div>' +
        '</div>' +
        '<div class="actions">' +
          '<button class="action" id="theme-toggle">' + (state.theme === 'light' ? 'Switch to Dark' : 'Switch to Light') + '</button>' +
          '<button class="action" id="reveal-change">Next Change</button>' +
          '<button class="action" id="open-source-beside">Open Beside</button>' +
          '<button class="action" id="open-source">Open Source File</button>' +
        '</div>' +
      '</div>';

      const toolbar = '<div class="editor-toolbar"><span>Augmented source view</span><span>' + payload.totalLines + ' lines</span></div>';
      const editor = '<div class="editor-shell">' + toolbar + '<div class="editor">' + payload.lines.map(renderRow).join('') + '</div></div>';
      app.innerHTML = header + editor;

      const changeRows = Array.from(document.querySelectorAll('.row[data-line].paired-block-anchor'));

      document.getElementById('reveal-change').addEventListener('click', () => {
        if (changeRows.length === 0) {
          return;
        }
        const currentIndex = typeof state.changeIndex === 'number' ? state.changeIndex : -1;
        activateChange(changeRows, currentIndex + 1);
      });

      document.querySelectorAll('.pair-jump').forEach(button => {
        button.addEventListener('click', event => {
          event.stopPropagation();
          if (changeRows.length === 0) {
            return;
          }
          const anchorLine = Number(button.getAttribute('data-anchor-line') || '0');
          const currentIndex = changeRows.findIndex(row => Number(row.getAttribute('data-line') || '0') === anchorLine);
          activateChange(changeRows, currentIndex + 1);
        });
      });

      document.getElementById('open-source').addEventListener('click', () => {
        vscode.postMessage({ type: 'openSource' });
      });

      document.getElementById('open-source-beside').addEventListener('click', () => {
        vscode.postMessage({ type: 'openSourceSideBySide' });
      });

      document.getElementById('theme-toggle').addEventListener('click', () => {
        state.theme = state.theme === 'light' ? 'dark' : 'light';
        vscode.setState(state);
        render();
      });

      document.querySelectorAll('.row[data-line]').forEach(element => {
        element.classList.add('line-clickable');
        element.addEventListener('click', () => {
          const changedIndex = changeRows.indexOf(element);
          if (changedIndex >= 0) {
            activateChange(changeRows, changedIndex);
            return;
          }
          document.querySelectorAll('.row.is-active').forEach(active => active.classList.remove('is-active'));
          element.classList.add('is-active');
        });
        element.addEventListener('dblclick', () => {
          const value = Number(element.getAttribute('data-line') || '0');
          if (value > 0) {
            vscode.postMessage({ type: 'openSourceSideBySide', lineNumber: value });
          }
        });
      });

      if (typeof state.changeIndex === 'number' && changeRows.length > 0) {
        activateChange(changeRows, state.changeIndex);
      }
    }

    render();
  </script>
</body>
</html>`;
  }

  private renderMissingHtml(): string {
    return `<!DOCTYPE html><html lang="en"><body style="background:#0f1117;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;padding:24px;">Unable to locate diff data for this turn.</body></html>`;
  }
}
