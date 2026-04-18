declare module 'space-data-module-sdk/testing/browser' {
  export interface BrowserModuleHarness {
    invoke: (request: unknown) => Promise<unknown>;
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
  }

  export function createBrowserModuleHarness(
    options: CreateBrowserModuleHarnessOptions,
  ): Promise<BrowserModuleHarness>;
}
