import { createRouteBundle } from 'redux-bundler'
import StatusPage from '../../../../../../webui/src/status/LoadableStatusPage.js'
import FilesPage from '../../../../../../webui/src/files/LoadableFilesPage.js'
import PinsPage from '../../../../../../webui/src/pins/LoadablePinsPage.js'
import PeersPage from '../../../../../../webui/src/peers/LoadablePeersPage.js'
import SettingsPage from '../settings/SettingsPage.js'
import AnalyticsPage from '../../../../../../webui/src/settings/AnalyticsPage.js'
import WelcomePage from '../../../../../../webui/src/welcome/LoadableWelcomePage.js'
import BlankPage from '../../../../../../webui/src/blank/BlankPage.js'
import ExplorePageRenderer from '../../../../../../webui/src/explore/explore-page-renderer.jsx'
import DiagnosticsPage from '../../../../../../webui/src/diagnostics/loadable-diagnostics-page'
import DirectoryPage from '../directory/DirectoryPage.js'

export default createRouteBundle({
  '/explore': ExplorePageRenderer,
  '/explore*': ExplorePageRenderer,
  '/files*': FilesPage,
  '/ipfs*': FilesPage,
  '/ipns*': FilesPage,
  '/pins*': PinsPage,
  '/peers': PeersPage,
  '/settings/analytics': AnalyticsPage,
  '/settings*': SettingsPage,
  '/welcome': WelcomePage,
  '/blank': BlankPage,
  '/diagnostics*': DiagnosticsPage,
  '/directory': DirectoryPage,
  '/status*': StatusPage,
  '/': StatusPage,
  '': StatusPage
}, { routeInfoSelector: 'selectHash' })
