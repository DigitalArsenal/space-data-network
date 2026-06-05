import {
  createAvailableResult,
  createDegradedResult,
  type BackendResult,
  type ChannelActionOptions,
  type ChannelBackend,
} from './sdn-backend';
import {
  createChannelFlatSqlIngestor,
  type ChannelFlatSqlIngestorOptions,
} from './channel-ingest';
import { assertChannelStreamMatchesStandardCode } from './channel-stream-standard';

export interface ChannelFlatSqlSyncResult {
  channelId: string;
  rows: ReturnType<typeof createChannelFlatSqlIngestor>['rows'];
  stats: ReturnType<typeof createChannelFlatSqlIngestor>['stats'] extends () => infer T ? T : never;
}

export interface ChannelFlatSqlSyncOptions extends ChannelFlatSqlIngestorOptions {
  access?: ChannelActionOptions;
}

export async function ingestChannelStreamToFlatSql(
  channels: Pick<ChannelBackend, 'openStream'>,
  channelId: string,
  options: ChannelFlatSqlSyncOptions = {},
): Promise<BackendResult<ChannelFlatSqlSyncResult>> {
  const stream = await channels.openStream(channelId, options.access);
  if (!stream.ok || !stream.data) {
    return createDegradedResult('channels.ingestFlatSql', stream.capability.reason ?? 'channel stream unavailable');
  }
  const { access: _access, ...ingestorOptions } = options;
  assertChannelStreamMatchesStandardCode(channelId, stream.data);
  const ingestor = createChannelFlatSqlIngestor(ingestorOptions);
  ingestor.pushChunk(stream.data);
  ingestor.finish();
  return createAvailableResult('channels.ingestFlatSql', {
    channelId,
    rows: ingestor.rows,
    stats: ingestor.stats(),
  });
}
