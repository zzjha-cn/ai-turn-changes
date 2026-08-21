import * as vscode from 'vscode';
import { TurnRecord } from '../../types';
import * as path from 'path';
import * as fs from 'fs';
import * as crypto from 'crypto';

export class TurnStore {
  private storagePath: string;
  private nextTurnIdFile: string;
  private historyFile: string;

  constructor(context: vscode.ExtensionContext, workspaceRoot: string) {
    // 每一个 workspaceRoot 的 hash 确定唯一的隔离存储目录
    const workspaceHash = crypto.createHash('md5').update(workspaceRoot).digest('hex');
    this.storagePath = path.join(context.globalStorageUri.fsPath, workspaceHash);

    if (!fs.existsSync(this.storagePath)) {
      fs.mkdirSync(this.storagePath, { recursive: true });
    }

    this.nextTurnIdFile = path.join(this.storagePath, 'next_turn_id.json');
    this.historyFile = path.join(this.storagePath, 'history.json');
  }

  /**
   * 获取并累加下一个自增的 TurnID
   */
  public getNextTurnId(): number {
    if (!fs.existsSync(this.nextTurnIdFile)) {
      fs.writeFileSync(this.nextTurnIdFile, JSON.stringify({ nextTurnId: 1 }));
      return 1;
    }
    try {
      const data = JSON.parse(fs.readFileSync(this.nextTurnIdFile, 'utf8'));
      return data.nextTurnId || 1;
    } catch {
      return 1;
    }
  }

  /**
   * 更新下一个自增 TurnID
   */
  public incrementNextTurnId(currentId: number): void {
    fs.writeFileSync(this.nextTurnIdFile, JSON.stringify({ nextTurnId: currentId + 1 }));
  }

  /**
   * 保存一轮 Turn 到本地历史
   */
  public saveTurn(record: TurnRecord): void {
    const history = this.getHistory();
    history.push(record);
    let removedRecord: TurnRecord | undefined;

    const maxHistory = 100;
    if (history.length > maxHistory) {
      removedRecord = history.shift();
    }

    fs.writeFileSync(this.historyFile, JSON.stringify(history, null, 2));
    if (removedRecord) {
      this.writeRemovedTurn(removedRecord);
    } else {
      this.clearRemovedTurn();
    }
  }

  /**
   * 获取历史轮次记录列表
   */
  public getHistory(): TurnRecord[] {
    if (!fs.existsSync(this.historyFile)) {
      return [];
    }
    try {
      return JSON.parse(fs.readFileSync(this.historyFile, 'utf8')) || [];
    } catch {
      return [];
    }
  }

  public replaceHistory(history: TurnRecord[]): void {
    fs.writeFileSync(this.historyFile, JSON.stringify(history, null, 2));
    this.clearRemovedTurn();
  }

  public removeTurn(turnId: number): TurnRecord | undefined {
    const history = this.getHistory();
    const index = history.findIndex(record => record.turnId === turnId);
    if (index === -1) {
      return undefined;
    }

    const [removed] = history.splice(index, 1);
    fs.writeFileSync(this.historyFile, JSON.stringify(history, null, 2));
    return removed;
  }

  /**
   * 清空本地历史记录
   */
  public clearHistory(): void {
    if (fs.existsSync(this.historyFile)) {
      fs.unlinkSync(this.historyFile);
    }
    if (fs.existsSync(this.nextTurnIdFile)) {
      fs.writeFileSync(this.nextTurnIdFile, JSON.stringify({ nextTurnId: 1 }));
    }
    this.clearRemovedTurn();
  }

  public consumeLastRemovedTurn(): TurnRecord | undefined {
    const removedTurnFile = path.join(this.storagePath, 'removed_turn.json');
    if (!fs.existsSync(removedTurnFile)) {
      return undefined;
    }
    try {
      const record = JSON.parse(fs.readFileSync(removedTurnFile, 'utf8')) as TurnRecord;
      fs.unlinkSync(removedTurnFile);
      return record;
    } catch {
      return undefined;
    }
  }

  private writeRemovedTurn(record: TurnRecord): void {
    const removedTurnFile = path.join(this.storagePath, 'removed_turn.json');
    fs.writeFileSync(removedTurnFile, JSON.stringify(record, null, 2));
  }

  private clearRemovedTurn(): void {
    const removedTurnFile = path.join(this.storagePath, 'removed_turn.json');
    if (fs.existsSync(removedTurnFile)) {
      fs.unlinkSync(removedTurnFile);
    }
  }
}
