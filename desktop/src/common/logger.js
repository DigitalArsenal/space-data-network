// @ts-check
const { createLogger, format, transports } = require('winston')
const { join } = require('node:path')
const { app } = require('electron')

const { combine, timestamp, printf, errors, splat } = format

// Logs live in <userData>/logs/ so "Show logs" in the tray can open one
// directory that holds both the shell's log and the node's stdout/stderr.
const logsPath = join(app.getPath('userData'), 'logs')

const logger = createLogger({
  format: combine(
    errors({ stack: true }),
    timestamp(),
    splat(),
    printf(info => `${info.timestamp} ${info.level}: ${info.message}`)
  ),
  transports: [
    new transports.Console({ level: 'debug', silent: process.env.NODE_ENV === 'production' }),
    new transports.File({ level: 'error', filename: join(logsPath, 'error.log') }),
    new transports.File({ level: 'debug', filename: join(logsPath, 'desktop.log') })
  ]
})

module.exports = Object.freeze({
  /** @param {string} msg */
  info: (msg) => { logger.info(msg) },
  /** @param {string} msg */
  debug: (msg) => { logger.debug(msg) },
  /** @param {string} msg */
  warn: (msg) => { logger.warn(msg) },
  /** @param {Error|string} err */
  error: (err) => { logger.error(err instanceof Error ? err : new Error(String(err))) },
  logsPath
})
