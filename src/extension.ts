import * as vscode from 'vscode';
import { GitWorkspaceService } from './core/git/GitWorkspaceService';
import { BaselineStore } from './core/baseline/BaselineStore';
import { DiffEngine } from './core/diff/DiffEngine';
import { TurnStore } from './core/storage/TurnStore';
import { SnapshotStore } from './core/storage/SnapshotStore';
import { TurnManager } from './core/turn/TurnManager';
import { TreeViewProvider, FileNode, TurnNode } from './ui/tree/TreeViewProvider';
import { TerminalBindingService } from './core/terminal/TerminalBindingService';
import { QuietWindowBoundaryDetector } from './core/terminal/QuietWindowBoundaryDetector';
import { TerminalMonitor } from './core/terminal/TerminalMonitor';
import { FileWatchService } from './core/storage/FileWatchService';
import * as path from 'path';
import { TurnDiffEditorProvider } from './ui/editor/TurnDiffEditorProvider';

function getDiffEditorTheme(): 'dark' | 'light' {
  const configured = vscode.workspace.getConfiguration('aiTurnChanges').get<string>('diffEditorTheme', 'dark');
  return configured === 'light' ? 'light' : 'dark';
}

function toUserFacingErrorMessage(error: unknown): string {
  const message = error instanceof Error ? error.message : String(error);
  if (message.includes('Snapshot not found:')) {
    return '当前轮次的基线快照已失效，请丢弃当前轮次后重新开始。';
  }
  return message;
}

export async function activate(context: vscode.ExtensionContext) {
  const treeViewProvider = new TreeViewProvider(undefined, undefined, 'Please open a folder to use AI Turn Changes.');
  const treeViewRegistration = vscode.window.registerTreeDataProvider('turnChangesView', treeViewProvider);
  context.subscriptions.push(treeViewRegistration);

  let initializedWorkspaceRoot: string | undefined;
  let workspaceResourcesDisposable: vscode.Disposable | undefined;

  const initializeWorkspaceServices = async (): Promise<void> => {
    const workspaceFolder = vscode.workspace.workspaceFolders?.[0];
    if (!workspaceFolder) {
      treeViewProvider.setPlaceholderMessage('Please open a folder to use AI Turn Changes.');
      return;
    }

    const workspaceRoot = workspaceFolder.uri.fsPath;
    if (initializedWorkspaceRoot === workspaceRoot) {
      return;
    }

    workspaceResourcesDisposable?.dispose();

    const snapshotRoot = path.join(context.globalStorageUri.fsPath, 'snapshots');
    const gitService = new GitWorkspaceService(workspaceRoot);
    const snapshotStore = new SnapshotStore(snapshotRoot);
    const baselineStore = new BaselineStore(gitService, snapshotStore);
    const diffEngine = new DiffEngine(gitService, snapshotStore, workspaceRoot);
    const turnStore = new TurnStore(context, workspaceRoot);
    const turnManager = new TurnManager(gitService, baselineStore, diffEngine, snapshotStore, turnStore, workspaceRoot);
    const terminalBinding = new TerminalBindingService();
    const quietDetector = new QuietWindowBoundaryDetector(1200, 1000);
    const terminalMonitor = new TerminalMonitor(terminalBinding, quietDetector);
    const fileWatchService = new FileWatchService(
      quietDetector,
      gitService,
      uri => {
        const boundTerminal = terminalBinding.getBoundTerminal();
        if (!boundTerminal || turnManager.getMode() !== 'auto') {
          return false;
        }
        return vscode.workspace.getWorkspaceFolder(uri)?.uri.fsPath === workspaceRoot;
      }
    );
    turnManager.setMode('auto', quietDetector);
    treeViewProvider.attachServices(turnManager, terminalBinding);

    const turnDiffEditorProvider = new TurnDiffEditorProvider(
      snapshotStore,
      turnId => turnManager.getHistory().find(record => record.turnId === turnId),
      async (_record, change, lineNumber) => {
        const absolutePath = path.join(workspaceRoot, change.path);
        const fileUri = vscode.Uri.file(absolutePath);
        const doc = await vscode.workspace.openTextDocument(fileUri);
        const editor = await vscode.window.showTextDocument(doc, { preview: false });
        if (lineNumber && lineNumber > 0) {
          const targetLine = Math.min(Math.max(lineNumber - 1, 0), Math.max(doc.lineCount - 1, 0));
          const position = new vscode.Position(targetLine, 0);
          const range = new vscode.Range(position, position);
          editor.selection = new vscode.Selection(position, position);
          editor.revealRange(range, vscode.TextEditorRevealType.InCenter);
        }
      },
      () => getDiffEditorTheme(),
      workspaceRoot
    );

    const turnDiffRefreshOnEdit = vscode.workspace.onDidChangeTextDocument(async event => {
      const eventWorkspaceFolder = vscode.workspace.getWorkspaceFolder(event.document.uri);
      if (!eventWorkspaceFolder || eventWorkspaceFolder.uri.fsPath !== workspaceRoot) {
        return;
      }
      const relativePath = path.relative(workspaceRoot, event.document.uri.fsPath).replace(/\\/g, '/');
      await turnDiffEditorProvider.refreshForWorkspaceFile(relativePath);
    });

    const turnDiffContentProviderRegistration = vscode.workspace.registerTextDocumentContentProvider(
      TurnDiffEditorProvider.scheme,
      turnDiffEditorProvider
    );
    const customEditorRegistration = vscode.window.registerCustomEditorProvider(
      TurnDiffEditorProvider.viewType,
      turnDiffEditorProvider,
      {
        webviewOptions: {
          retainContextWhenHidden: true
        },
        supportsMultipleEditorsPerDocument: false
      }
    );

    const startManualTurnCommand = vscode.commands.registerCommand('turnChanges.startManualTurn', async () => {
      try {
        await turnManager.startManualTurn();
        vscode.window.showInformationMessage('AI Turn started! (Manual Mode). Run your AI CLI agent command now.');
      } catch (e: any) {
        vscode.window.showErrorMessage(e.message);
      }
    });

    const finishManualTurnCommand = vscode.commands.registerCommand('turnChanges.finishManualTurn', async () => {
      try {
        vscode.window.withProgress({
          location: vscode.ProgressLocation.Notification,
          title: 'Calculating Turn Changes...',
          cancellable: false
        }, async () => {
          const record = await turnManager.finishManualTurn();
          if (record) {
            treeViewProvider.selectTurn(record);
          }
        });
      } catch (e: any) {
        vscode.window.showErrorMessage(toUserFacingErrorMessage(e));
      }
    });

    const discardTurnCommand = vscode.commands.registerCommand('turnChanges.discardTurn', () => {
      turnManager.discardActiveTurn();
      vscode.window.showInformationMessage('AI Turn recording discarded.');
    });

    const bindTerminalCommand = vscode.commands.registerCommand('turnChanges.bindTerminal', () => {
      const term = terminalBinding.bindActiveTerminal();
      if (term) {
        vscode.window.showInformationMessage(`Bound to terminal: ${term.name}`);
      } else {
        vscode.window.showWarningMessage('No active terminal to bind. Open a terminal first.');
      }
    });

    const unbindTerminalCommand = vscode.commands.registerCommand('turnChanges.unbindTerminal', () => {
      terminalBinding.unbind();
      vscode.window.showInformationMessage('Terminal unbound.');
    });

    const setManualModeCommand = vscode.commands.registerCommand('turnChanges.setManualMode', () => {
      turnManager.setMode('manual');
      vscode.window.showInformationMessage('Switched to Manual mode.');
    });

    const setAutoModeCommand = vscode.commands.registerCommand('turnChanges.setAutoMode', () => {
      turnManager.setMode('auto', quietDetector);
      vscode.window.showInformationMessage('Switched to Automatic mode.');
    });

    const mergeTurnDownCommand = vscode.commands.registerCommand('turnChanges.mergeTurnDown', (node?: { record?: { turnId: number } }) => {
      const turnId = node?.record?.turnId;
      if (!turnId) {
        vscode.window.showWarningMessage('Please select a turn to merge.');
        return;
      }
      const merged = turnManager.mergeTurnDown(turnId);
      if (!merged) {
        vscode.window.showWarningMessage('Cannot merge this turn downward.');
        return;
      }
      treeViewProvider.selectTurn(merged);
      vscode.window.showInformationMessage(`Turn #${turnId} merged into Turn #${merged.turnId}.`);
    });

    const selectTurnCommand = vscode.commands.registerCommand('turnChanges.selectTurn', (node?: TurnNode) => {
      if (!node?.record) {
        return;
      }
      treeViewProvider.selectTurn(node.record);
    });

    const removeTurnCommand = vscode.commands.registerCommand('turnChanges.removeTurn', (node?: { record?: { turnId: number } }) => {
      const turnId = node?.record?.turnId;
      if (!turnId) {
        vscode.window.showWarningMessage('Please select a turn to remove.');
        return;
      }
      const removed = turnManager.removeTurn(turnId);
      if (!removed) {
        vscode.window.showWarningMessage('Cannot remove this turn.');
        return;
      }
      vscode.window.showInformationMessage(`Turn #${turnId} removed.`);
    });

    const removeTurnsFromCommand = vscode.commands.registerCommand('turnChanges.removeTurnsFrom', async (node?: { record?: { turnId: number } }) => {
      const turnId = node?.record?.turnId;
      if (!turnId) {
        vscode.window.showWarningMessage('Please select a turn to remove older turns from.');
        return;
      }
      const confirmed = await vscode.window.showWarningMessage(
        `Remove all turns older than Turn #${turnId}?`,
        { modal: true },
        'Remove'
      );
      if (confirmed !== 'Remove') {
        return;
      }
      const removed = turnManager.removeTurnsFrom(turnId);
      if (removed.length === 0) {
        vscode.window.showWarningMessage('No older turns can be removed from this point.');
        return;
      }
      vscode.window.showInformationMessage(`Removed ${removed.length} older turn(s) before Turn #${turnId}.`);
    });

    const openFileChangeCommand = vscode.commands.registerCommand('turnChanges.openFileChange', async (fileNode: FileNode) => {
      if (fileNode.change.status === 'deleted') {
        vscode.window.showInformationMessage('This file was deleted in this turn.');
        return;
      }

      try {
        await turnDiffEditorProvider.openDiffForChange(fileNode.record, fileNode.change);
      } catch (e: any) {
        vscode.window.showErrorMessage(`Failed to open file: ${e.message}`);
      }
    });

    workspaceResourcesDisposable = vscode.Disposable.from(
      startManualTurnCommand,
      finishManualTurnCommand,
      discardTurnCommand,
      bindTerminalCommand,
      unbindTerminalCommand,
      setManualModeCommand,
      setAutoModeCommand,
      mergeTurnDownCommand,
      selectTurnCommand,
      removeTurnCommand,
      removeTurnsFromCommand,
      openFileChangeCommand,
      turnDiffContentProviderRegistration,
      customEditorRegistration,
      turnDiffRefreshOnEdit,
      terminalMonitor,
      fileWatchService
    );
    context.subscriptions.push(workspaceResourcesDisposable);
    initializedWorkspaceRoot = workspaceRoot;
  };

  const refreshWorkspaceBinding = async (): Promise<void> => {
    const workspaceFolder = vscode.workspace.workspaceFolders?.[0];
    if (!workspaceFolder) {
      initializedWorkspaceRoot = undefined;
      workspaceResourcesDisposable?.dispose();
      workspaceResourcesDisposable = undefined;
      treeViewProvider.setPlaceholderMessage('Please open a folder to use AI Turn Changes.');
      return;
    }
    await initializeWorkspaceServices();
  };

  context.subscriptions.push(vscode.workspace.onDidChangeWorkspaceFolders(() => {
    void refreshWorkspaceBinding();
  }));
  context.subscriptions.push(vscode.window.onDidChangeActiveTextEditor(() => {
    void refreshWorkspaceBinding();
  }));

  await refreshWorkspaceBinding();
}

export function deactivate() { }
