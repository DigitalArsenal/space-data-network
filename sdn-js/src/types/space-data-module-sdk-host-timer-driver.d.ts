declare module 'space-data-module-sdk/host/timer-driver' {
  export interface ModuleTimerRunRecord {
    timerId: string;
    methodId: string;
    trigger: 'scheduled' | 'manual';
    status: 'running' | 'ok' | 'error' | 'skipped';
    startedAt: number;
    finishedAt?: number;
    message?: string;
  }

  export interface ModuleTimerInfo {
    timerId: string;
    methodId: string;
    description: string;
    defaultIntervalMs: number;
    intervalMs: number;
    enabled: boolean;
    scheduled: boolean;
  }

  export interface ModuleTimerDriver {
    listTimers(): ModuleTimerInfo[];
    runHistory(timerId: string): ModuleTimerRunRecord[];
    start(): ModuleTimerInfo[];
    runNow(timerId: string): Promise<unknown>;
    stop(): void;
  }

  export function createModuleTimerDriver(options: {
    harness?: { invoke(request: unknown): Promise<unknown> };
    invoke?: (request: {
      methodId: string;
      inputs: unknown[];
    }) => Promise<unknown>;
    manifestBytes?: Uint8Array;
    manifest?: Record<string, unknown>;
    timers?: Array<Record<string, unknown>>;
    schedules?: Record<string, { enabled?: boolean; intervalMs?: number }>;
    minIntervalMs?: number;
    maxRunHistory?: number;
    setIntervalImpl?: (fn: () => void, ms: number) => unknown;
    clearIntervalImpl?: (handle: unknown) => void;
    now?: () => number;
    onRun?: (run: ModuleTimerRunRecord) => void;
  }): ModuleTimerDriver;
}
