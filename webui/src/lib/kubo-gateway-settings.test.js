import { kuboGatewaySettingFromUrl } from './kubo-gateway-settings.js'

describe('kubo gateway settings', () => {
  it('converts the desktop gateway query parameter for the IPLD explorer', () => {
    expect(kuboGatewaySettingFromUrl('http://127.0.0.1:17890/sdn/?gateway=http%3A%2F%2F127.0.0.1%3A8081')).toEqual({
      host: '127.0.0.1',
      port: '8081',
      protocol: 'http',
      trustlessBlockBrokerConfig: {
        init: {
          allowLocal: true,
          allowInsecure: false
        }
      }
    })
  })

  it('ignores missing or invalid gateway parameters', () => {
    expect(kuboGatewaySettingFromUrl('http://127.0.0.1:17890/sdn/')).toBeNull()
    expect(kuboGatewaySettingFromUrl('http://127.0.0.1:17890/sdn/?gateway=ftp%3A%2F%2Fexample.test')).toBeNull()
  })
})
