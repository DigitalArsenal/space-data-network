/**
 * @packageDocumentation
 *
 * This module allows easy conversion of Multiaddrs to string URIs.
 *
 * @example Converting multiaddrs to string URIs
 *
 * ```js
 * import { multiaddrToUri } from '@multiformats/multiaddr-to-uri'
 *
 * console.log(multiaddrToUri('/dnsaddr/protocol.ai/https'))
 * // -> https://protocol.ai
 *
 * console.log(multiaddrToUri('/ip4/127.0.0.1/tcp/8080'))
 * // -> http://127.0.0.1:8080
 *
 * console.log(multiaddrToUri('/ip4/127.0.0.1/tcp/8080', { assumeHttp: false }))
 * // -> tcp://127.0.0.1:8080
 * ```
 *
 * Note:
 *
 * - When `/tcp` is the last (terminating) protocol HTTP is assumed by default (implicit `assumeHttp: true`)
 *   - this means produced URIs will start with `http://` instead of `tcp://`
 *   - passing `{ assumeHttp: false }` disables this behavior
 * - Might be lossy - e.g. a DNSv6 multiaddr
 * - Can throw if the passed multiaddr:
 *   - is not a valid multiaddr
 *   - is not supported as a URI e.g. circuit
 */

import { multiaddr, protocols } from '@multiformats/multiaddr'
import type { Multiaddr, StringTuple } from '@multiformats/multiaddr'

export interface MultiaddrToUriOpts {
  assumeHttp?: boolean
}

const ASSUME_HTTP_CODES = [
  protocols('tcp').code,
  protocols('dns').code,
  protocols('dnsaddr').code,
  protocols('dns4').code,
  protocols('dns6').code
]

interface Interpreter { (value: string, ma: StringTuple[]): string }

function extractSNI (ma: StringTuple[]): string | undefined {
  return extractTuple('sni', ma)?.[1]
}

function extractPort (ma: StringTuple[]): string {
  const port = extractTuple('tcp', ma)?.[1]

  if (port == null) {
    return ''
  }

  return `:${port}`
}

function extractTuple (name: string, ma: StringTuple[]): StringTuple | undefined {
  let code: number

  try {
    code = protocols(name).code
  } catch (e) {
    // No support for protocol in multiaddr
    return
  }

  for (const [proto, value] of ma) {
    if (proto === code && value != null) {
      return [proto, value]
    }
  }
}

function hasTLS (ma: StringTuple[]): boolean {
  return ma.some(([proto, _]) => proto === protocols('tls').code)
}

function interpretNext (headProtoCode: number, headProtoVal: string, restMa: StringTuple[]): string {
  const interpreter = interpreters[protocols(headProtoCode).name]
  if (interpreter == null) {
    throw new Error(`Can't interpret protocol ${protocols(headProtoCode).name}`)
  }
  const restVal = interpreter(headProtoVal, restMa)
  if (headProtoCode === protocols('ip6').code) {
    return `[${restVal}]`
  }
  return restVal
}

const interpreters: Record<string, Interpreter> = {
  ip4: (value: string, restMa: StringTuple[]) => value,
  ip6: (value: string, restMa: StringTuple[]) => {
    if (restMa.length === 0) {
      return value
    }
    return `[${value}]`
  },
  tcp: (value: string, restMa: StringTuple[]) => {
    const tailProto = restMa.pop()
    if (tailProto == null) {
      throw new Error('Unexpected end of multiaddr')
    }
    return `tcp://${interpretNext(tailProto[0], tailProto[1] ?? '', restMa)}:${value}`
  },
  udp: (value: string, restMa: StringTuple[]) => {
    const tailProto = restMa.pop()
    if (tailProto == null) {
      throw new Error('Unexpected end of multiaddr')
    }
    return `udp://${interpretNext(tailProto[0], tailProto[1] ?? '', restMa)}:${value}`
  },
  dnsaddr: (value: string, restMa: StringTuple[]) => value,
  dns4: (value: string, restMa: StringTuple[]) => value,
  dns6: (value: string, restMa: StringTuple[]) => value,
  dns: (value: string, restMa: StringTuple[]) => value,
  ipfs: (value: string, restMa: StringTuple[]) => {
    const tailProto = restMa.pop()
    if (tailProto == null) {
      throw new Error('Unexpected end of multiaddr')
    }
    return `${interpretNext(tailProto[0], tailProto[1] ?? '', restMa)}/ipfs/${value}`
  },
  p2p: (value: string, restMa: StringTuple[]) => {
    const tailProto = restMa.pop()
    if (tailProto == null) {
      throw new Error('Unexpected end of multiaddr')
    }
    return `${interpretNext(tailProto[0], tailProto[1] ?? '', restMa)}/p2p/${value}`
  },
  http: (value: string, restMa: StringTuple[]) => {
    const maHasTLS = hasTLS(restMa)
    const sni = extractSNI(restMa)
    const port = extractPort(restMa)
    if (maHasTLS && sni != null) {
      return `https://${sni}${port}`
    }
    const protocol = maHasTLS ? 'https://' : 'http://'
    const tailProto = restMa.pop()
    if (tailProto == null) {
      throw new Error('Unexpected end of multiaddr')
    }
    let baseVal = interpretNext(tailProto[0], tailProto[1] ?? '', restMa)
    // We are reinterpreting the base as http, so we need to remove the tcp:// if it's there
    baseVal = baseVal.replace('tcp://', '')
    return `${protocol}${baseVal}`
  },
  'http-path': (value: string, restMa: StringTuple[]) => {
    const tailProto = restMa.pop()
    if (tailProto == null) {
      throw new Error('Unexpected end of multiaddr')
    }
    const baseVal = interpretNext(tailProto[0], tailProto[1] ?? '', restMa)
    const decodedValue = decodeURIComponent(value)
    return `${baseVal}/${decodedValue}`
  },
  tls: (value: string, restMa: StringTuple[]) => {
    // Noop, the parent context knows that it's tls. We don't need to do
    // anything here
    const tailProto = restMa.pop()
    if (tailProto == null) {
      throw new Error('Unexpected end of multiaddr')
    }
    return interpretNext(tailProto[0], tailProto[1] ?? '', restMa)
  },
  sni: (value: string, restMa: StringTuple[]) => {
    // Noop, the parent context uses the sni information, we don't need to do
    // anything here
    const tailProto = restMa.pop()
    if (tailProto == null) {
      throw new Error('Unexpected end of multiaddr')
    }
    return interpretNext(tailProto[0], tailProto[1] ?? '', restMa)
  },
  https: (value: string, restMa: StringTuple[]) => {
    const tailProto = restMa.pop()
    if (tailProto == null) {
      throw new Error('Unexpected end of multiaddr')
    }
    let baseVal = interpretNext(tailProto[0], tailProto[1] ?? '', restMa)
    // We are reinterpreting the base as http, so we need to remove the tcp:// if it's there
    baseVal = baseVal.replace('tcp://', '')
    return `https://${baseVal}`
  },
  ws: (value: string, restMa: StringTuple[]) => {
    const maHasTLS = hasTLS(restMa)
    const sni = extractSNI(restMa)
    const port = extractPort(restMa)
    if (maHasTLS && sni != null) {
      return `wss://${sni}${port}`
    }
    const protocol = maHasTLS ? 'wss://' : 'ws://'
    const tailProto = restMa.pop()
    if (tailProto == null) {
      throw new Error('Unexpected end of multiaddr')
    }
    let baseVal = interpretNext(tailProto[0], tailProto[1] ?? '', restMa)
    // We are reinterpreting the base, so we need to remove the tcp:// if it's there
    baseVal = baseVal.replace('tcp://', '')
    return `${protocol}${baseVal}`
  },
  wss: (value: string, restMa: StringTuple[]) => {
    const tailProto = restMa.pop()
    if (tailProto == null) {
      throw new Error('Unexpected end of multiaddr')
    }
    let baseVal = interpretNext(tailProto[0], tailProto[1] ?? '', restMa)
    // We are reinterpreting the base as http, so we need to remove the tcp:// if it's there
    baseVal = baseVal.replace('tcp://', '')
    return `wss://${baseVal}`
  },
  'p2p-websocket-star': (value: string, restMa: StringTuple[]) => {
    const tailProto = restMa.pop()
    if (tailProto == null) {
      throw new Error('Unexpected end of multiaddr')
    }
    return `${interpretNext(tailProto[0], tailProto[1] ?? '', restMa)}/p2p-websocket-star`
  },
  'p2p-webrtc-star': (value: string, restMa: StringTuple[]) => {
    const tailProto = restMa.pop()
    if (tailProto == null) {
      throw new Error('Unexpected end of multiaddr')
    }
    return `${interpretNext(tailProto[0], tailProto[1] ?? '', restMa)}/p2p-webrtc-star`
  },
  'p2p-webrtc-direct': (value: string, restMa: StringTuple[]) => {
    const tailProto = restMa.pop()
    if (tailProto == null) {
      throw new Error('Unexpected end of multiaddr')
    }
    return `${interpretNext(tailProto[0], tailProto[1] ?? '', restMa)}/p2p-webrtc-direct`
  }
}

export function multiaddrToUri (input: Multiaddr | string | Uint8Array, opts?: MultiaddrToUriOpts): string {
  const ma = multiaddr(input)
  const parts = ma.stringTuples()
  const head = parts.pop()
  if (head == null) {
    throw new Error('Unexpected end of multiaddr')
  }

  const protocol = protocols(head[0])
  const interpreter = interpreters[protocol.name]

  if (interpreter == null) {
    throw new Error(`No interpreter found for ${protocol.name}`)
  }

  let uri = interpreter(head[1] ?? '', parts)

  if (opts?.assumeHttp !== false && ASSUME_HTTP_CODES.includes(head[0])) {
    // strip any declared protocol
    uri = uri.replace(/^.*:\/\//, '')

    if (head[1] === '443') {
      uri = `https://${uri}`
    } else {
      uri = `http://${uri}`
    }
  }

  if (uri.startsWith('http://') || uri.startsWith('https://') || uri.startsWith('ws://') || uri.startsWith('wss://')) {
    // this will strip default ports while keeping paths intact
    uri = new URL(uri).toString()

    // strip trailing slash, e.g. http://127.0.0.1/ -> http://127.0.0.1
    if (uri.endsWith('/')) {
      uri = uri.substring(0, uri.length - 1)
    }
  }

  return uri
}
