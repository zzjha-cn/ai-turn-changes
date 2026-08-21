import * as cp from 'child_process';
import * as fs from 'fs';
import * as path from 'path';

export class GitWorkspaceService {
  private workspaceRoot: string;

  constructor(workspaceRoot: string) {
    this.workspaceRoot = workspaceRoot;
  }

  public async isGitRepository(): Promise<boolean> {
    try {
      await this.runGitCommand(['rev-parse', '--is-inside-work-tree']);
      return true;
    } catch {
      return false;
    }
  }

  public async getGitRoot(): Promise<string> {
    const output = await this.runGitCommand(['rev-parse', '--show-toplevel']);
    return output.trim();
  }

  public async getTrackedFiles(): Promise<string[]> {
    const output = await this.runGitCommand(['ls-files']);
    return output.split('\n').map(f => f.trim()).filter(f => f.length > 0);
  }

  public async getUntrackedFiles(): Promise<string[]> {
    const output = await this.runGitCommand(['ls-files', '--others', '--exclude-standard']);
    return output.split('\n').map(f => f.trim()).filter(f => f.length > 0);
  }

  public async listTurnCandidateFiles(): Promise<string[]> {
    const tracked = await this.getTrackedFiles();
    const untracked = await this.getUntrackedFiles();
    return Array.from(new Set([...tracked, ...untracked])).sort();
  }

  public async isIgnored(relativePath: string): Promise<boolean> {
    try {
      await this.runGitCommand(['check-ignore', relativePath]);
      return true;
    } catch {
      return false;
    }
  }

  public resolveAbsolutePath(relativePath: string): string {
    return path.join(this.workspaceRoot, relativePath);
  }

  public isBinaryFile(relativePath: string): boolean {
    const absolutePath = this.resolveAbsolutePath(relativePath);
    if (!fs.existsSync(absolutePath)) {
      return false;
    }

    const buffer = Buffer.alloc(512);
    const fd = fs.openSync(absolutePath, 'r');
    try {
      const bytesRead = fs.readSync(fd, buffer, 0, 512, 0);
      for (let i = 0; i < bytesRead; i++) {
        if (buffer[i] === 0) {
          return true;
        }
      }
    } finally {
      fs.closeSync(fd);
    }
    return false;
  }

  private runGitCommand(args: string[]): Promise<string> {
    return new Promise((resolve, reject) => {
      cp.execFile('git', args, { cwd: this.workspaceRoot, maxBuffer: 10 * 1024 * 1024 }, (error, stdout, stderr) => {
        if (error) {
          reject(new Error(stderr || error.message));
        } else {
          resolve(stdout);
        }
      });
    });
  }
}
