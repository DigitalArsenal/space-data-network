import {
  createAvailableResult,
  createDegradedResult,
  type BackendResult,
  type ChannelBackend,
} from './sdn-backend';
import {
  createChannelFlatSqlIngestor,
  type ChannelFlatSqlIngestorOptions,
} from './channel-ingest';

export interface ChannelFlatSqlSyncResult {
  channelId: string;
  rows: ReturnType<typeof createChannelFlatSqlIngestor>['rows'];
  stats: ReturnType<typeof createChannelFlatSqlIngestor>['stats'] extends () => infer T ? T : never;
}

export async function ingestChannelStreamToFlatSql(
  channels: Pick<ChannelBackend, 'openStream'>,
  channelId: string,
  options: ChannelFlatSqlIngestorOptions = {},
): Promise<BackendResult<ChannelFlatSqlSyncResult>> {
  const stream = await channels.openStream(channelId);
  if (!stream.ok || !stream.data) {
    return createDegradedResult('channels.ingestFlatSql', stream.capability.reason ?? 'channel stream unavailable');
  }
  const ingestor = createChannelFlatSqlIngestor(options);
  ingestor.pushChunk(stream.data);
  ingestor.finish();
  return createAvailableResult('channels.ingestFlatSql', {
    channelId,
    rows: ingestor.rows,
    stats: ingestor.stats(),
  });
}
