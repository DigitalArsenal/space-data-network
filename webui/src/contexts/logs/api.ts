export interface LogEntry {
  id: number
  timestamp: string
  level: string
  subsystem: string
  message: string
}

export interface LogSubsystemState {
  name: string
  level: string
}

export interface LogRateState {
  currentRate: number
  hasWarned: boolean
  autoDisabled: boolean
}

export interface LogStorageStats {
  totalEntries: number
  estimatedSize: number
}
