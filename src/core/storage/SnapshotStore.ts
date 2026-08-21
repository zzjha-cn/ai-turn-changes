import * as crypto from 'crypto';
import * as fs from 'fs';
import * as path from 'path';

type SnapshotMetadata = {
  references: Record<string, number>;
};

export class SnapshotStore {
  private snapshotRoot: string;
  private metadataPath: string;

  constructor(snapshotRoot: string) {
    this.snapshotRoot = snapshotRoot;
    this.metadataPath = path.join(this.snapshotRoot, 'metadata.json');
    fs.mkdirSync(this.snapshotRoot, { recursive: true });
    if (!fs.existsSync(this.metadataPath)) {
      fs.writeFileSync(this.metadataPath, JSON.stringify({ references: {} }, null, 2));
    }
  }

  public async storeFileContent(filePath: string): Promise<string> {
    const buffer = await fs.promises.readFile(filePath);
    return this.storeBuffer(buffer);
  }

  public async storeTextContent(content: string): Promise<string> {
    return this.storeBuffer(Buffer.from(content, 'utf8'));
  }

  public async readContent(snapshotId: string): Promise<string> {
    const snapshotPath = this.getSnapshotPath(snapshotId);
    try {
      return await fs.promises.readFile(snapshotPath, 'utf8');
    } catch (error: any) {
      if (error?.code === 'ENOENT') {
        throw new Error(`Snapshot not found: ${snapshotId}`);
      }
      throw error;
    }
  }

  public async readBuffer(snapshotId: string): Promise<Buffer> {
    const snapshotPath = this.getSnapshotPath(snapshotId);
    return fs.promises.readFile(snapshotPath);
  }

  public readContentSync(snapshotId: string): string {
    const snapshotPath = this.getSnapshotPath(snapshotId);
    try {
      return fs.readFileSync(snapshotPath, 'utf8');
    } catch (error: any) {
      if (error?.code === 'ENOENT') {
        throw new Error(`Snapshot not found: ${snapshotId}`);
      }
      throw error;
    }
  }

  public addReferences(snapshotIds: string[]): void {
    if (snapshotIds.length === 0) {
      return;
    }
    const metadata = this.readMetadata();
    for (const snapshotId of snapshotIds) {
      metadata.references[snapshotId] = (metadata.references[snapshotId] || 0) + 1;
    }
    this.writeMetadata(metadata);
  }

  public removeReferences(snapshotIds: string[]): void {
    if (snapshotIds.length === 0) {
      return;
    }
    const metadata = this.readMetadata();
    for (const snapshotId of snapshotIds) {
      const current = metadata.references[snapshotId] || 0;
      if (current <= 1) {
        delete metadata.references[snapshotId];
        const snapshotPath = this.getSnapshotPath(snapshotId);
        if (fs.existsSync(snapshotPath)) {
          fs.unlinkSync(snapshotPath);
        }
      } else {
        metadata.references[snapshotId] = current - 1;
      }
    }
    this.writeMetadata(metadata);
  }

  private async storeBuffer(buffer: Buffer): Promise<string> {
    const snapshotId = crypto.createHash('sha1').update(buffer).digest('hex');
    const snapshotPath = this.getSnapshotPath(snapshotId);
    if (!fs.existsSync(snapshotPath)) {
      await fs.promises.writeFile(snapshotPath, buffer);
    }
    return snapshotId;
  }

  private getSnapshotPath(snapshotId: string): string {
    return path.join(this.snapshotRoot, snapshotId);
  }

  private readMetadata(): SnapshotMetadata {
    try {
      return JSON.parse(fs.readFileSync(this.metadataPath, 'utf8')) as SnapshotMetadata;
    } catch {
      return { references: {} };
    }
  }

  private writeMetadata(metadata: SnapshotMetadata): void {
    fs.writeFileSync(this.metadataPath, JSON.stringify(metadata, null, 2));
  }
}
