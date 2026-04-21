import { getProvideStats } from './api'
import type { ProvideStats } from './types'

describe('getProvideStats', () => {
  it('reads provide stats from the routing provide stat endpoint', async () => {
    const stats: ProvideStats = { FullRT: true }
    const previousFetch = globalThis.fetch
    const fetchSpy = jest.fn().mockResolvedValue({
      ok: true,
      json: async () => stats
    } as Response)
    globalThis.fetch = fetchSpy as typeof fetch
    const ipfs = {
      getEndpointConfig: () => ({
        host: '127.0.0.1',
        port: '5001',
        protocol: 'http:',
        pathname: '/api/v0',
        'api-path': '/api/v0'
      })
    } as any

    await expect(getProvideStats(ipfs, { all: true, compact: true })).resolves.toBe(stats)
    expect(fetchSpy).toHaveBeenCalledWith(
      'http://127.0.0.1:5001/api/v0/routing/provide/stat?all=true&compact=true',
      expect.objectContaining({ method: 'POST' })
    )

    globalThis.fetch = previousFetch
  })
})
