import { writeSetting } from '../bundles/local-storage.js'

const TRUSTLESS_BLOCK_BROKER_CONFIG = {
  init: {
    allowLocal: true,
    allowInsecure: false
  }
}

export function kuboGatewaySettingFromUrl (urlString) {
  try {
    const url = new URL(urlString)
    const gateway = url.searchParams.get('gateway')
    if (!gateway) return null

    const gatewayUrl = new URL(gateway)
    if (gatewayUrl.protocol !== 'http:' && gatewayUrl.protocol !== 'https:') return null

    return {
      host: gatewayUrl.hostname,
      port: gatewayUrl.port,
      protocol: gatewayUrl.protocol.replace(/:$/, ''),
      trustlessBlockBrokerConfig: TRUSTLESS_BLOCK_BROKER_CONFIG
    }
  } catch (_) {
    return null
  }
}

export function syncKuboGatewaySettingFromUrl (urlString = window.location.href) {
  const setting = kuboGatewaySettingFromUrl(urlString)
  if (setting == null) return false

  writeSetting('kuboGateway', setting)
  return true
}
