/**
 * SDN Theme Bundle - Dark mode default with localStorage persistence
 *
 * Phase 17.1: Dark Mode Default
 * - Defaults to dark theme on first load
 * - Persists theme preference in localStorage
 * - Provides selectors and actions for theme toggling
 */
import { readSetting, writeSetting } from './local-storage.js'

const SDN_THEME_KEY = 'sdn-theme'
const DARK = 'dark'
const LIGHT = 'light'

const initialTheme = readSetting(SDN_THEME_KEY) || DARK

// Apply theme to document on load
if (typeof document !== 'undefined') {
  document.documentElement.setAttribute('data-theme', initialTheme)
}

const sdnThemeBundle = {
  name: 'sdnTheme',
  reducer (state = { theme: initialTheme }, action) {
    if (action.type === 'SDN_THEME_TOGGLE') {
      return { ...state, theme: state.theme === DARK ? LIGHT : DARK }
    }
    if (action.type === 'SDN_THEME_SET') {
      return { ...state, theme: action.payload }
    }
    return state
  },

  selectSdnTheme (state) {
    return state.sdnTheme.theme
  },

  selectIsDarkMode (state) {
    return state.sdnTheme.theme === DARK
  },

  doToggleSdnTheme () {
    return ({ dispatch, store }) => {
      const current = store.selectSdnTheme()
      const next = current === DARK ? LIGHT : DARK
      writeSetting(SDN_THEME_KEY, next)
      document.documentElement.setAttribute('data-theme', next)
      dispatch({ type: 'SDN_THEME_SET', payload: next })
    }
  },

  doSetSdnTheme (theme) {
    return ({ dispatch }) => {
      writeSetting(SDN_THEME_KEY, theme)
      document.documentElement.setAttribute('data-theme', theme)
      dispatch({ type: 'SDN_THEME_SET', payload: theme })
    }
  }
}

export default sdnThemeBundle
