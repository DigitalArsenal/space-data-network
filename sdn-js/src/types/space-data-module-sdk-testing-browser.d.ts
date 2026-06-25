declare module 'space-data-module-sdk/testing/browser' {
  export interface BrowserModuleHarness {
    invoke: (request: unknown) => Promise<unknown>;
    invokeDirect?: (request: unknown) => Promise<unknown>;
    runtime?: {
      surface?: 'direct' | 'command' | string;
    };
    memory?: WebAssembly.Memory;
  }

  export interface CreateBrowserModuleHarnessOptions {
    wasmSource: Uint8Array | ArrayBuffer | Response | string;
    host?: unknown;
    args?: string[];
    env?: Record<string, string>;
    surface?: 'direct' | 'command';
    hostOptions?: Record<string, unknown>;
    performance?: Performance;
    logOutput?: boolean;
    sharedMemory?: boolean;
    initialMemoryBytes?: number;
    maximumMemoryBytes?: number;
  }

  export function createBrowserModuleHarness(
    options: CreateBrowserModuleHarnessOptions,
  ): Promise<BrowserModuleHarness>;
}
