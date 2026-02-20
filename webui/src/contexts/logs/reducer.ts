export interface LogBufferConfig {
  memory: number
  indexedDB: number
  warnThreshold: number
  autoDisableThreshold: number
}

export const DEFAULT_LOG_BUFFER_CONFIG: LogBufferConfig = {
  memory: 500,
  indexedDB: 5000,
  warnThreshold: 50,
  autoDisableThreshold: 200
}

function sanitizeInteger(value: unknown, fallback: number, min: number): number {
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) {
    return fallback
  }
  return Math.max(min, Math.floor(parsed))
}

export function sanitizeLogBufferConfig(input: Partial<LogBufferConfig> | null | undefined): LogBufferConfig {
  const source = input ?? {}
  return {
    memory: sanitizeInteger(source.memory, DEFAULT_LOG_BUFFER_CONFIG.memory, 50),
    indexedDB: sanitizeInteger(source.indexedDB, DEFAULT_LOG_BUFFER_CONFIG.indexedDB, 100),
    warnThreshold: sanitizeInteger(source.warnThreshold, DEFAULT_LOG_BUFFER_CONFIG.warnThreshold, 1),
    autoDisableThreshold: sanitizeInteger(
      source.autoDisableThreshold,
      DEFAULT_LOG_BUFFER_CONFIG.autoDisableThreshold,
      1
    )
  }
}
