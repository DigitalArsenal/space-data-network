const { app, shell } = require('electron')
const getCtx = require('../context')

function normalizeInternalPath (parsedUrl) {
  const hashPath = parsedUrl.hash.replace(/^#/, '').trim()
  if (hashPath) {
    return hashPath
  }

  const pathname = parsedUrl.pathname.trim()
  if (pathname && pathname !== '/') {
    return pathname
  }

  return '/'
}

function appTargetForUrl (parsedUrl) {
  if (parsedUrl.protocol === 'webui:' && parsedUrl.host === '-') {
    return 'webui'
  }

  if (parsedUrl.protocol === 'sdn:' && parsedUrl.host === '-') {
    return 'sdn'
  }

  return null
}

function currentOriginForContents (contents) {
  try {
    const currentUrl = contents.getURL()
    if (!currentUrl) {
      return null
    }
    return appTargetForUrl(new URL(currentUrl))
  } catch {
    return null
  }
}

module.exports = function () {
  const ctx = getCtx()
  const launchWebUI = ctx.getFn('launchWebUI')
  const launchDashboard = ctx.getFn('launchDashboard')

  app.on('web-contents-created', (_, contents) => {
    contents.on('will-navigate', (event, url) => {
      const parsedUrl = new URL(url)
      const target = appTargetForUrl(parsedUrl)
      const currentOrigin = currentOriginForContents(contents)

      if (
        target &&
        (currentOrigin === null || currentOrigin === target)
      ) {
        return
      }

      if (target === 'webui') {
        event.preventDefault()
        launchWebUI(normalizeInternalPath(parsedUrl))
        return
      }

      if (target === 'sdn') {
        event.preventDefault()
        launchDashboard(normalizeInternalPath(parsedUrl))
        return
      }

      event.preventDefault()
      shell.openExternal(url)
    })

    // handling external links
    contents.setWindowOpenHandler(({ url }) => {
      const parsedUrl = new URL(url)
      const target = appTargetForUrl(parsedUrl)
      if (target === 'webui') {
        launchWebUI(normalizeInternalPath(parsedUrl))
        return { action: 'deny' }
      }

      if (target === 'sdn') {
        launchDashboard(normalizeInternalPath(parsedUrl))
        return { action: 'deny' }
      }

      // open in external URL handler (user's default web browser)
      shell.openExternal(url)
      // do not open in Electron itself
      return { action: 'deny' }
    })
  })
}
