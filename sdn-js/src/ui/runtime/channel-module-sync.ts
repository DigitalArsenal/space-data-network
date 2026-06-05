import {
  createAvailableResult,
  createDegradedResult,
  type BackendResult,
  type ChannelBackend,
} from './sdn-backend';
import {
  createChannelModuleStreamPump,
  type ChannelModuleStreamPumpOptions,
} from './channel-module-pump';

export interface ChannelModulePumpSyncResult {
  channelId: string;
  lastResponse: ReturnType<typeof createChannelModuleStreamPump>['finish'] extends () => Promise<infer T> ? T : never;
  stats: ReturnType<typeof createChannelModuleStreamPump>['stats'] extends () => infer T ? T : never;
}

export async function pumpChannelStreamToModule(
  channels: Pick<ChannelBackend, 'openStream'>,
  channelId: string,
  options: ChannelModuleStreamPumpOptions,
): Promise<BackendResult<ChannelModulePumpSyncResult>> {
  const stream = await channels.openStream(channelId);
  if (!stream.ok || !stream.data) {
    return createDegradedResult('channels.moduleFeed', stream.capability.reason ?? 'channel stream unavailable');
  }
  const pump = createChannelModuleStreamPump(options);
  await pump.pushChunk(stream.data);
  const lastResponse = await pump.finish();
  return createAvailableResult('channels.moduleFeed', {
    channelId,
    lastResponse,
    stats: pump.stats(),
  });
}
