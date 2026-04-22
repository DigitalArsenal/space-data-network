export function brandUpstreamDocumentTitle(title) {
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
    const brandedTitle = brandUpstreamDocumentTitle(document.title)
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
