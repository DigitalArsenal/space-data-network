import {
  createAvailableResult,
  createDegradedResult,
  type BackendResult,
  type ChannelActionOptions,
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

export interface ChannelModulePumpSyncOptions extends ChannelModuleStreamPumpOptions {
  access?: ChannelActionOptions;
}

export async function pumpChannelStreamToModule(
  channels: Pick<ChannelBackend, 'openStream'>,
  channelId: string,
  options: ChannelModulePumpSyncOptions,
): Promise<BackendResult<ChannelModulePumpSyncResult>> {
  const stream = await channels.openStream(channelId, options.access);
  if (!stream.ok || !stream.data) {
    return createDegradedResult('channels.moduleFeed', stream.capability.reason ?? 'channel stream unavailable');
  }
  const { access: _access, ...pumpOptions } = options;
  const pump = createChannelModuleStreamPump(pumpOptions);
  await pump.pushChunk(stream.data);
  const lastResponse = await pump.finish();
  return createAvailableResult('channels.moduleFeed', {
    channelId,
    lastResponse,
    stats: pump.stats(),
  });
}
