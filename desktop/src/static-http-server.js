// @ts-check
const fs = require('fs')
const path = require('path')
const http = require('http')
const { app } = require('electron')
const portfinder = require('portfinder')
const logger = require('./common/logger')

const HOST = '127.0.0.1'
const START_PORT = 17890
const ROUTES = Object.freeze({
  sdn: 'assets/sdn-ui',
  webui: 'assets/webui'
})

let serverPromise = null

const contentTypes = Object.freeze({
  '.css': 'text/css; charset=utf-8',
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.map': 'application/json; charset=utf-8',
  '.png': 'image/png',
  '.svg': 'image/svg+xml; charset=utf-8',
  '.wasm': 'application/wasm',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2'
})

function routeForUrl (requestUrl) {
  const parsed = new URL(requestUrl, `http://${HOST}`)
  const [, routeName, ...segments] = parsed.pathname.split('/')
  const routeDirectory = ROUTES[routeName]

  if (!routeDirectory) {
    return null
  }

  const root = path.resolve(app.getAppPath(), routeDirectory)
  const requestedPath = path.resolve(root, ...segments)

  if (!requestedPath.startsWith(root)) {
    return null
  }

  if (fs.existsSync(requestedPath) && fs.statSync(requestedPath).isFile()) {
    return requestedPath
  }

  return path.join(root, 'index.html')
}

function serveFile (res, filePath) {
  fs.readFile(filePath, (err, body) => {
    if (err) {
      res.writeHead(404)
      res.end('Not found')
      return
    }

    res.writeHead(200, {
      'Content-Type': contentTypes[path.extname(filePath)] || 'application/octet-stream',
      'Cache-Control': 'no-store'
    })
    res.end(body)
  })
}

async function startDesktopStaticServer () {
  if (serverPromise) {
    return serverPromise
  }

  serverPromise = (async () => {
    const port = await portfinder.getPortPromise({ port: START_PORT })
    const server = http.createServer((req, res) => {
      const filePath = routeForUrl(req.url || '/')

      if (!filePath) {
        res.writeHead(404)
        res.end('Not found')
        return
      }

      serveFile(res, filePath)
    })

    await new Promise((resolve, reject) => {
      server.once('error', reject)
      server.listen(port, HOST, resolve)
    })

    const origin = `http://${HOST}:${port}`
    logger.info(`[desktop static] serving SDN and WebUI assets at ${origin}`)

    app.once('before-quit', () => {
      server.close()
    })

    return { origin, server }
  })()

  return serverPromise
}

async function getDesktopStaticOrigin () {
  const { origin } = await startDesktopStaticServer()
  return origin
}

async function getDesktopStaticUrl (routeName, hash = '/') {
  const origin = await getDesktopStaticOrigin()
  const url = new URL(`/${routeName}/`, origin)
  url.hash = hash
  return url
}

module.exports = {
  getDesktopStaticOrigin,
  getDesktopStaticUrl,
  startDesktopStaticServer
}
