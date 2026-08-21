import * as vscode from 'vscode';
import { TurnBoundaryDetector } from '../terminal/TurnBoundaryDetector';
import { GitWorkspaceService } from '../git/GitWorkspaceService';

export class FileWatchService {
  private detector: TurnBoundaryDetector;
  private gitService: GitWorkspaceService;
  private fileWatcher?: vscode.FileSystemWatcher;
  private disposables: vscode.Disposable[] = [];

  constructor(detector: TurnBoundaryDetector, gitService: GitWorkspaceService) {
    this.detector = detector;
    this.gitService = gitService;
    this.startWatching();
  }

  /**
   * 启动文件系统监听，排除 node_modules 等大体积噪音文件夹
   */
  private startWatching(): void {
    this.fileWatcher = vscode.workspace.createFileSystemWatcher('**/*');

    const handleFileEvent = async (uri: vscode.Uri, type: 'create' | 'change' | 'delete') => {
      const fsPath = uri.fsPath;
      if (fsPath.includes('node_modules') || fsPath.includes('.git') || fsPath.includes('out') || fsPath.includes('dist')) {
        return;
      }

      const workspaceFolder = vscode.workspace.getWorkspaceFolder(uri);
      if (!workspaceFolder) {
        return;
      }

      const relativePath = vscode.workspace.asRelativePath(uri, false);
      if (await this.gitService.isIgnored(relativePath)) {
        return;
      }

      this.detector.onFileChange({
        uri,
        type
      });
    };

    this.fileWatcher.onDidCreate(uri => {
      void handleFileEvent(uri, 'create');
    }, null, this.disposables);
    this.fileWatcher.onDidChange(uri => {
      void handleFileEvent(uri, 'change');
    }, null, this.disposables);
    this.fileWatcher.onDidDelete(uri => {
      void handleFileEvent(uri, 'delete');
    }, null, this.disposables);
  }

  public dispose(): void {
    if (this.fileWatcher) {
      this.fileWatcher.dispose();
    }
    this.disposables.forEach(d => d.dispose());
  }
}
