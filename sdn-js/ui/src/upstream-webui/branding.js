const rootOnlyRouteTitles = new Map([
  ['/directory', 'Directory | Space Data Network'],
  ['/modules', 'Modules | Space Data Network'],
])

export function rootOnlyDocumentTitleForHash(hash) {
  const route = String(hash ?? '').replace(/^#/, '')
  for (const [prefix, title] of rootOnlyRouteTitles) {
    if (route === prefix || route.startsWith(`${prefix}/`)) {
      return title
    }
  }
  return null
}

export function brandUpstreamDocumentTitle(title, hash = '') {
  const rootOnlyTitle = rootOnlyDocumentTitleForHash(hash)
  if (rootOnlyTitle) {
    return rootOnlyTitle
  }

  const trimmedTitle = String(title ?? '').trim()
  if (!trimmedTitle) {
    return 'Space Data Network'
  }
  if (trimmedTitle.includes('Space Data Network')) {
    return trimmedTitle
  }
  if (trimmedTitle === 'IPFS') {
    return 'Space Data Network'
  }
  return trimmedTitle.replace(/\s*\|\s*IPFS$/, ' | Space Data Network')
}

export function installRootDocumentTitleSync() {
  if (typeof window === 'undefined' || window.__sdnTitleSyncInstalled) {
    return
  }

  const applyBranding = () => {
    const brandedTitle = brandUpstreamDocumentTitle(document.title, window.location.hash)
    if (document.title !== brandedTitle) {
      document.title = brandedTitle
    }
  }

  applyBranding()

  const observer = new MutationObserver(() => {
    applyBranding()
  })
  observer.observe(document.head, {
    childList: true,
    subtree: true,
    characterData: true,
  })

  window.addEventListener('hashchange', applyBranding)
  window.__sdnTitleSyncInstalled = true
}
