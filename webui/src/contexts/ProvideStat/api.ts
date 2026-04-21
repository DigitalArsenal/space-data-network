import type { KuboRPCClient } from 'kubo-rpc-client'
import type { ProvideStatOptions, ProvideStats } from './types'

export async function getProvideStats (
  ipfs: KuboRPCClient,
  options: ProvideStatOptions = { all: true }
): Promise<ProvideStats> {
  const endpoint = ipfs.getEndpointConfig()
  const basePath = normalizeAPIPath(endpoint['api-path'] || endpoint.pathname)
  const url = new URL(`${endpoint.protocol}//${endpoint.host}${formatPort(endpoint.port)}${basePath}/routing/provide/stat`)

  Object.entries(options).forEach(([key, value]) => {
    if (key === 'signal' || key === 'headers' || value == null) {
      return
    }
    url.searchParams.set(key, String(value))
  })

  const response = await fetch(url.toString(), {
    method: 'POST',
    headers: options.headers,
    signal: options.signal
  })

  if (!response.ok) {
    throw new Error(`provide stat request failed (${response.status})`)
  }

  return await response.json() as ProvideStats
}

function normalizeAPIPath (pathname: string): string {
  const trimmed = pathname.trim()
  if (trimmed === '') {
    return '/api/v0'
  }
  return '/' + trimmed.replace(/^\/+/, '').replace(/\/+$/, '')
}

function formatPort (port: string): string {
  return port ? `:${port}` : ''
}
