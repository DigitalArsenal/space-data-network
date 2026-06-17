import {
  createModuleFlatBufferStreamPump,
} from 'space-data-module-sdk/testing/module-flatbuffer-stream-pump';
import type {
  ModuleFlatBufferStreamPumpStats,
  PluginInvokeRequestEnvelope,
  PluginInvokeResponseEnvelope,
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
  const pump = createModuleFlatBufferStreamPump({
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
    async finish() {
      return (await pump.finish()) as PluginInvokeResponseEnvelope | null;
    },
    stats() {
      return { ...pump.stats } as ModuleFlatBufferStreamPumpStats;
    },
  };
}
