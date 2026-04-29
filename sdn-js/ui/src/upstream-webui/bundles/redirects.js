import { createSelector } from 'redux-bundler'

function canStayOnFailedIpfs(hash) {
  return hash === '/welcome' || hash.startsWith('/settings') || hash === '/modules'
}

const redirectsBundle = {
  name: 'redirects',

  reactToEmptyHash: createSelector(
    'selectHash',
    (hash) => {
      if (hash === '') {
        return { actionCreator: 'doUpdateHash', args: ['#/'] }
      }
    }
  ),

  reactToIpfsConnectionFail: createSelector(
    'selectIpfsInitFailed',
    'selectHash',
    (failed, hash) => {
      if (failed && !canStayOnFailedIpfs(hash)) {
        return { actionCreator: 'doUpdateHash', args: ['#/welcome'] }
      }
    }
  )
}

export default redirectsBundle
