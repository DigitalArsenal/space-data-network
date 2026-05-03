// @ts-check
const fs = require('fs')
const path = require('path')
const { app, session } = require('electron')
const { promisify } = require('util')

const stat = promisify(fs.stat)
const FILE_NOT_FOUND = -6

async function getPath (filePath) {
  try {
    const result = await stat(filePath)

    if (result.isFile()) {
      return filePath
    }

    if (result.isDirectory()) {
      return getPath(path.join(filePath, 'index.html'))
    }
  } catch (_) {}
}

function registerStaticScheme ({ scheme, directory, partition }) {
  const resolvedDirectory = path.resolve(app.getAppPath(), directory)

  const handler = async (request, callback) => {
    const indexPath = path.join(resolvedDirectory, 'index.html')
    const filePath = path.join(resolvedDirectory, decodeURIComponent(new URL(request.url).pathname))
    const resolvedPath = await getPath(filePath)
    const fileExtension = path.extname(filePath)

    if (resolvedPath || !fileExtension || fileExtension === '.html' || fileExtension === '.asar') {
      callback({ path: resolvedPath || indexPath })
    } else {
      callback({ error: FILE_NOT_FOUND })
    }
  }

  const register = () => {
    const targetSession = partition
      ? session.fromPartition(partition)
      : session.defaultSession

    targetSession.protocol.registerFileProtocol(scheme, handler)
  }

  if (app.isReady()) {
    register()
  } else {
    app.on('ready', register)
  }
}

module.exports = registerStaticScheme
