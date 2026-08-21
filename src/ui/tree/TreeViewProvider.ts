import * as vscode from 'vscode';
import { TurnManager } from '../../core/turn/TurnManager';
import { TurnRecord, FileChange } from '../../types';
import { TerminalBindingService } from '../../core/terminal/TerminalBindingService';

type TreeNode = SectionNode | StatusNode | ActionNode | TurnNode | TurnActionNode | FileNode;

export class TreeViewProvider implements vscode.TreeDataProvider<TreeNode> {
  private turnManager: TurnManager;
  private terminalBinding?: TerminalBindingService;
  private selectedRecord?: TurnRecord;

  private onDidChangeTreeDataEmitter = new vscode.EventEmitter<TreeNode | undefined | void>();
  public readonly onDidChangeTreeData = this.onDidChangeTreeDataEmitter.event;

  constructor(turnManager: TurnManager, terminalBinding?: TerminalBindingService) {
    this.turnManager = turnManager;
    this.terminalBinding = terminalBinding;

    this.turnManager.onStateChanged(() => this.refresh());
    if (this.terminalBinding) {
      this.terminalBinding.onBindingChanged(() => this.refresh());
    }
  }

  public refresh(): void {
    this.onDidChangeTreeDataEmitter.fire();
  }

  public selectTurn(record: TurnRecord): void {
    this.selectedRecord = record;
    this.refresh();
  }

  public getTreeItem(element: TreeNode): vscode.TreeItem {
    return element;
  }

  public async getChildren(element?: TreeNode): Promise<TreeNode[]> {
    if (!element) {
      return [
        new SectionNode('Session', 'sessionSection', vscode.TreeItemCollapsibleState.Expanded),
        new SectionNode('Actions', 'actionsSection', vscode.TreeItemCollapsibleState.Expanded),
        new SectionNode('Turns', 'turnsSection', vscode.TreeItemCollapsibleState.Expanded)
      ];
    }

    if (element instanceof SectionNode) {
      if (element.sectionType === 'sessionSection') {
        return this.getSessionNodes();
      }
      if (element.sectionType === 'actionsSection') {
        return this.getActionNodes();
      }
      if (element.sectionType === 'turnsSection') {
        return this.getTurnNodes();
      }
    }

    if (element instanceof TurnNode) {
      return [
        new TurnActionNode('Merge Down', 'turnChanges.mergeTurnDown', 'combine', element.record),
        new TurnActionNode('Remove', 'turnChanges.removeTurn', 'trash', element.record),
        ...element.record.changes.map(change => new FileNode(change, element.record))
      ];
    }

    return [];
  }

  private getSessionNodes(): TreeNode[] {
    const mode = this.turnManager.getMode();
    const activeCandidate = this.turnManager.getActiveCandidate();
    const nodes: TreeNode[] = [
      new StatusNode(
        `Mode: ${mode === 'auto' ? 'Automatic' : 'Manual'}`,
        'modeStatus',
        mode === 'auto' ? 'run-all' : 'edit',
        mode === 'auto' ? 'Current mode is Automatic' : 'Current mode is Manual'
      ),
      new StatusNode(
        activeCandidate ? 'Turn State: Recording' : 'Turn State: Ready',
        'turnStateStatus',
        activeCandidate ? 'record' : 'circle-large-outline',
        activeCandidate ? 'A turn is currently being recorded' : 'No turn is currently recording'
      )
    ];

    if (this.terminalBinding) {
      const boundTerminal = this.terminalBinding.getBoundTerminal();
      nodes.push(new StatusNode(
        boundTerminal ? `Terminal: ${boundTerminal.name}` : 'Terminal: Unbound',
        'terminalStatus',
        boundTerminal ? 'plug' : 'debug-disconnect',
        boundTerminal ? 'Currently bound terminal' : 'No terminal is currently bound'
      ));
    }

    return nodes;
  }

  private getActionNodes(): TreeNode[] {
    const activeCandidate = this.turnManager.getActiveCandidate();
    const mode = this.turnManager.getMode();
    const nodes: TreeNode[] = [];

    if (activeCandidate) {
      nodes.push(new ActionNode('Finish Current Turn', 'turnChanges.finishManualTurn', 'check', 'actionNode'));
      nodes.push(new ActionNode('Discard Current Turn', 'turnChanges.discardTurn', 'close', 'actionNode'));
    } else {
      nodes.push(new ActionNode('Start Manual Turn', 'turnChanges.startManualTurn', 'play', 'actionNode'));
    }

    nodes.push(new ActionNode(
      mode === 'auto' ? 'Switch to Manual Mode' : 'Switch to Automatic Mode',
      mode === 'auto' ? 'turnChanges.setManualMode' : 'turnChanges.setAutoMode',
      mode === 'auto' ? 'edit' : 'run-all',
      'actionNode'
    ));

    if (this.terminalBinding?.getBoundTerminal()) {
      nodes.push(new ActionNode('Unbind Terminal', 'turnChanges.unbindTerminal', 'debug-disconnect', 'actionNode'));
    } else {
      nodes.push(new ActionNode('Bind Active Terminal', 'turnChanges.bindTerminal', 'plug', 'actionNode'));
    }

    return nodes;
  }

  private getTurnNodes(): TreeNode[] {
    const history = this.turnManager.getHistory();
    if (history.length === 0) {
      return [new StatusNode('No turn history found.', 'emptyTurns', 'history')];
    }

    const nodes: TreeNode[] = [];
    for (let i = history.length - 1; i >= 0; i--) {
      const rec = history[i];
      const isSelected = this.selectedRecord?.turnId === rec.turnId;
      nodes.push(new TurnNode(rec, isSelected ? vscode.TreeItemCollapsibleState.Expanded : vscode.TreeItemCollapsibleState.Collapsed));
    }
    return nodes;
  }
}

export class SectionNode extends vscode.TreeItem {
  public readonly sectionType: string;

  constructor(label: string, sectionType: string, collapsibleState: vscode.TreeItemCollapsibleState) {
    super(label, collapsibleState);
    this.sectionType = sectionType;
    this.contextValue = sectionType;
  }
}

export class StatusNode extends vscode.TreeItem {
  constructor(
    label: string,
    contextValue: string,
    iconId?: string,
    tooltip?: string
  ) {
    super(label, vscode.TreeItemCollapsibleState.None);
    this.contextValue = contextValue;
    if (iconId) {
      this.iconPath = new vscode.ThemeIcon(iconId);
    }
    if (tooltip) {
      this.tooltip = tooltip;
    }
  }
}

export class ActionNode extends vscode.TreeItem {
  constructor(label: string, commandId: string, iconId: string, contextValue: string) {
    super(label, vscode.TreeItemCollapsibleState.None);
    this.contextValue = contextValue;
    this.iconPath = new vscode.ThemeIcon(iconId);
    this.command = {
      command: commandId,
      title: label
    };
  }
}

export class TurnNode extends vscode.TreeItem {
  public readonly record: TurnRecord;

  constructor(record: TurnRecord, collapsibleState: vscode.TreeItemCollapsibleState) {
    const label = `Turn #${record.turnId}`;
    super(label, collapsibleState);
    this.record = record;
    this.contextValue = 'turnNode';
    this.tooltip = `Created at ${new Date(record.createdAt).toLocaleString()}`;
    this.description = `${record.source === 'auto' ? 'Auto' : 'Manual'} · ${record.summary.filesChanged} files · +${record.summary.insertions} -${record.summary.deletions}`;
    this.command = {
      command: 'turnChanges.selectTurn',
      title: 'Select Turn',
      arguments: [this]
    };
  }
}

export class TurnActionNode extends vscode.TreeItem {
  public readonly record: TurnRecord;

  constructor(label: string, commandId: string, iconId: string, record: TurnRecord) {
    super(label, vscode.TreeItemCollapsibleState.None);
    this.record = record;
    this.contextValue = 'turnActionNode';
    this.iconPath = new vscode.ThemeIcon(iconId);
    this.command = {
      command: commandId,
      title: label,
      arguments: [{ record }]
    };
  }
}

export class FileNode extends vscode.TreeItem {
  public readonly change: FileChange;
  public readonly record: TurnRecord;

  constructor(change: FileChange, record: TurnRecord) {
    super(change.path, vscode.TreeItemCollapsibleState.None);
    this.change = change;
    this.record = record;
    this.contextValue = 'fileNode';

    if (change.status === 'added') {
      this.description = 'Added';
      this.iconPath = new vscode.ThemeIcon('file-add', new vscode.ThemeColor('gitDecoration.addedResourceForeground'));
    } else if (change.status === 'deleted') {
      this.description = 'Deleted';
      this.iconPath = new vscode.ThemeIcon('file-sub', new vscode.ThemeColor('gitDecoration.deletedResourceForeground'));
    } else {
      this.description = 'Modified';
      this.iconPath = new vscode.ThemeIcon('file-code', new vscode.ThemeColor('gitDecoration.modifiedResourceForeground'));
    }

    this.command = {
      command: 'turnChanges.openFileChange',
      title: 'Open File Change',
      arguments: [this]
    };
  }
}
