import {
  createModuleFlatBufferStreamPump,
  type ModuleFlatBufferStreamPump,
  type ModuleFlatBufferStreamPumpStats,
  type PluginInvokeRequestEnvelope,
  type PluginInvokeResponseEnvelope,
} from 'space-data-module-sdk';

export interface ChannelModuleStreamPump {
  pushChunk(chunk: Uint8Array | ArrayBuffer | ArrayBufferView): Promise<number>;
  finish(): Promise<PluginInvokeResponseEnvelope | null>;
  stats(): ModuleFlatBufferStreamPumpStats;
}

export interface ChannelModuleStreamPumpOptions {
  methodId: string;
  portId: string;
  maxFramesPerInvoke?: number;
  streamId?: number;
  sequenceStart?: number;
  invoke(request: PluginInvokeRequestEnvelope): Promise<PluginInvokeResponseEnvelope>;
}

export function createChannelModuleStreamPump(options: ChannelModuleStreamPumpOptions): ChannelModuleStreamPump {
  const pump: ModuleFlatBufferStreamPump = createModuleFlatBufferStreamPump({
    methodId: options.methodId,
    portId: options.portId,
    maxFramesPerInvoke: options.maxFramesPerInvoke,
    streamId: options.streamId,
    sequenceStart: options.sequenceStart,
    invoke: options.invoke,
  });
  return {
    pushChunk(chunk) {
      return pump.pushBytes(chunk);
    },
    finish() {
      return pump.finish();
    },
    stats() {
      return { ...pump.stats };
    },
  };
}
