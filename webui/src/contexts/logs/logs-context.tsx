import React, {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState
} from 'react'

import type {
  LogEntry,
  LogRateState,
  LogStorageStats,
  LogSubsystemState
} from './api'
import {
  DEFAULT_LOG_BUFFER_CONFIG,
  sanitizeLogBufferConfig,
  type LogBufferConfig
} from './reducer'
import { calculateGologLevelString, subsystemsToActualLevels, type LogSubsystem } from '../../lib/golog-level-utils'

const BUFFER_CONFIG_STORAGE_KEY = 'sdn:webui:logs-buffer-config'

const DEFAULT_SUBSYSTEM_NAMES = [
  'bitswap',
  'dht',
  'swarm',
  'provider',
  'relay',
  'pubsub'
]

const SAMPLE_MESSAGES = [
  'peer routing table refreshed',
  'bitswap session tick',
  'provider announcement completed',
  'swarm connection stabilized',
  'content retrieval diagnostic event'
]

interface LogsContextValue {
  entries: LogEntry[]
  isStreaming: boolean
  bufferConfig: LogBufferConfig
  rateState: LogRateState
  storageStats: LogStorageStats | null
  gologLevelString: string | null
  subsystems: LogSubsystemState[]
  isAgentVersionSupported: boolean
  startStreaming: () => void
  stopStreaming: () => void
  clearEntries: () => void
  showWarning: () => void
  updateBufferConfig: (config: LogBufferConfig) => void
  setLogLevelsBatch: (levels: Array<{ subsystem: string, level: string }>) => Promise<void>
}

const LogsContext = createContext<LogsContextValue | undefined>(undefined)

function loadInitialBufferConfig(): LogBufferConfig {
  if (typeof window === 'undefined') {
    return DEFAULT_LOG_BUFFER_CONFIG
  }

  try {
    const raw = window.localStorage.getItem(BUFFER_CONFIG_STORAGE_KEY)
    if (!raw) {
      return DEFAULT_LOG_BUFFER_CONFIG
    }
    return sanitizeLogBufferConfig(JSON.parse(raw))
  } catch {
    return DEFAULT_LOG_BUFFER_CONFIG
  }
}

function createBaseSubsystems(globalLevel: string): LogSubsystemState[] {
  return DEFAULT_SUBSYSTEM_NAMES.map((name) => ({ name, level: globalLevel }))
}

function randomSample<T>(values: T[]): T {
  const index = Math.floor(Math.random() * values.length)
  return values[index]
}

export const LogsProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [entries, setEntries] = useState<LogEntry[]>([])
  const [isStreaming, setIsStreaming] = useState(false)
  const [bufferConfig, setBufferConfig] = useState<LogBufferConfig>(() => loadInitialBufferConfig())
  const [rateState, setRateState] = useState<LogRateState>({
    currentRate: 0,
    hasWarned: false,
    autoDisabled: false
  })
  const [gologLevelString, setGologLevelString] = useState<string>('error')
  const [subsystems, setSubsystems] = useState<LogSubsystemState[]>(() => createBaseSubsystems('error'))

  const nextEntryIdRef = useRef(1)
  const streamTimerRef = useRef<number | null>(null)
  const timestampsRef = useRef<number[]>([])
  const bufferConfigRef = useRef(bufferConfig)

  useEffect(() => {
    bufferConfigRef.current = bufferConfig
    if (typeof window !== 'undefined') {
      window.localStorage.setItem(BUFFER_CONFIG_STORAGE_KEY, JSON.stringify(bufferConfig))
    }
  }, [bufferConfig])

  const stopStreaming = useCallback(() => {
    if (streamTimerRef.current !== null && typeof window !== 'undefined') {
      window.clearInterval(streamTimerRef.current)
      streamTimerRef.current = null
    }
    setIsStreaming(false)
    setRateState((current) => ({
      ...current,
      currentRate: 0
    }))
    timestampsRef.current = []
  }, [])

  const pushEntry = useCallback((entry: Omit<LogEntry, 'id'>) => {
    const now = Date.now()
    timestampsRef.current.push(now)
    timestampsRef.current = timestampsRef.current.filter((timestamp) => now - timestamp <= 1000)

    const currentRate = timestampsRef.current.length
    const thresholdConfig = bufferConfigRef.current
    const autoDisabled = currentRate > thresholdConfig.autoDisableThreshold

    setEntries((current) => {
      const next: LogEntry[] = [
        ...current,
        {
          ...entry,
          id: nextEntryIdRef.current++
        }
      ]

      if (next.length > thresholdConfig.memory) {
        return next.slice(next.length - thresholdConfig.memory)
      }
      return next
    })

    setRateState((current) => ({
      ...current,
      currentRate,
      autoDisabled: current.autoDisabled || autoDisabled
    }))

    if (autoDisabled) {
      stopStreaming()
    }
  }, [stopStreaming])

  const startStreaming = useCallback(() => {
    if (streamTimerRef.current !== null || typeof window === 'undefined') {
      setIsStreaming(true)
      return
    }

    setIsStreaming(true)
    setRateState((current) => ({
      ...current,
      autoDisabled: false
    }))

    streamTimerRef.current = window.setInterval(() => {
      const timestamp = new Date().toISOString()
      const subsystem = randomSample(DEFAULT_SUBSYSTEM_NAMES)
      const level = randomSample(['info', 'warn', 'error', 'debug'])
      const message = randomSample(SAMPLE_MESSAGES)
      pushEntry({
        timestamp,
        level,
        subsystem,
        message
      })
    }, 1000)
  }, [pushEntry])

  useEffect(() => {
    return () => {
      if (streamTimerRef.current !== null && typeof window !== 'undefined') {
        window.clearInterval(streamTimerRef.current)
      }
    }
  }, [])

  const clearEntries = useCallback(() => {
    setEntries([])
    timestampsRef.current = []
    setRateState((current) => ({
      ...current,
      currentRate: 0,
      autoDisabled: false
    }))
  }, [])

  const showWarning = useCallback(() => {
    setRateState((current) => ({
      ...current,
      hasWarned: true
    }))
  }, [])

  const updateBufferConfig = useCallback((nextConfig: LogBufferConfig) => {
    setBufferConfig(sanitizeLogBufferConfig(nextConfig))
  }, [])

  const setLogLevelsBatch = useCallback(async (levels: Array<{ subsystem: string, level: string }>) => {
    const normalized = levels
      .map((level) => ({
        subsystem: String(level.subsystem || '').trim(),
        level: String(level.level || '').trim().toLowerCase()
      }))
      .filter((level) => level.subsystem.length > 0 && level.level.length > 0)

    if (normalized.length === 0) {
      return
    }

    let globalLevel = 'error'
    for (const { subsystem, level } of normalized) {
      if (subsystem === '*' || subsystem === '(default)') {
        globalLevel = level
      }
    }

    setSubsystems((current) => {
      const levelsBySubsystem = new Map<string, string>()
      for (const item of current) {
        levelsBySubsystem.set(item.name, item.level)
      }

      for (const item of normalized) {
        if (item.subsystem === '*' || item.subsystem === '(default)') {
          continue
        }
        levelsBySubsystem.set(item.subsystem, item.level)
      }

      const merged = [...levelsBySubsystem.entries()]
        .map(([name, level]) => ({ name, level }))
        .sort((a, b) => a.name.localeCompare(b.name))

      const gologInput: LogSubsystem[] = [
        { subsystem: '*', level: globalLevel },
        ...merged.map((item) => ({ subsystem: item.name, level: item.level }))
      ]

      const nextGologValue = calculateGologLevelString(subsystemsToActualLevels(gologInput))
      setGologLevelString(nextGologValue ?? globalLevel)
      return merged
    })
  }, [])

  const storageStats = useMemo<LogStorageStats>(() => {
    const estimatedSize = entries.reduce((sum, entry) => {
      return (
        sum +
        entry.timestamp.length +
        entry.level.length +
        entry.subsystem.length +
        entry.message.length +
        16
      )
    }, 0)

    return {
      totalEntries: entries.length,
      estimatedSize
    }
  }, [entries])

  const value = useMemo<LogsContextValue>(() => ({
    entries,
    isStreaming,
    bufferConfig,
    rateState,
    storageStats,
    gologLevelString,
    subsystems,
    isAgentVersionSupported: true,
    startStreaming,
    stopStreaming,
    clearEntries,
    showWarning,
    updateBufferConfig,
    setLogLevelsBatch
  }), [
    entries,
    isStreaming,
    bufferConfig,
    rateState,
    storageStats,
    gologLevelString,
    subsystems,
    startStreaming,
    stopStreaming,
    clearEntries,
    showWarning,
    updateBufferConfig,
    setLogLevelsBatch
  ])

  return (
    <LogsContext.Provider value={value}>
      {children}
    </LogsContext.Provider>
  )
}

export function useLogs(): LogsContextValue {
  const context = useContext(LogsContext)
  if (!context) {
    throw new Error('useLogs must be used inside LogsProvider')
  }
  return context
}
