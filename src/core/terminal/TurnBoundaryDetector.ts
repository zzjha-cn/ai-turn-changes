import * as vscode from 'vscode';

export interface TerminalOutputEvent {
  terminal: vscode.Terminal;
  data: string;
  kind?: 'output' | 'commandStart' | 'commandEnd' | 'prompt' | 'awaitingInput' | 'continuationPrompt';
}

export interface WorkspaceFileChangeEvent {
  uri: vscode.Uri;
  type: 'create' | 'change' | 'delete';
}

export interface BoundaryDecision {
  action: 'complete' | 'discard';
  reason?: string;
}

export interface TurnBoundaryDetector {
  onTerminalOutput(event: TerminalOutputEvent): void;
  onFileChange(event: WorkspaceFileChangeEvent): void;
  tryCompleteCandidate(): BoundaryDecision | null;
  reset(): void;
}
