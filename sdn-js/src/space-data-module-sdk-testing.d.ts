declare module 'space-data-module-sdk/testing' {
  export interface ModuleFlatBufferStreamPumpStats {
    bytesReceived: number;
    chunksReceived: number;
    framesDecoded: number;
    framesInvoked: number;
    invokes: number;
    parseErrors: number;
  }

  export interface ModuleFlatBufferStreamPump {
    stats: ModuleFlatBufferStreamPumpStats;
    lastResponse: unknown;
    pushBytes(data: Uint8Array | ArrayBuffer | ArrayBufferView): Promise<number>;
    finish(): Promise<unknown>;
  }

  export function createModuleFlatBufferStreamPump(options: {
    methodId: string;
    portId: string;
    maxFramesPerInvoke?: number;
    streamId?: number;
    sequenceStart?: number;
    invoke(request: unknown): Promise<unknown>;
  }): ModuleFlatBufferStreamPump;
}
