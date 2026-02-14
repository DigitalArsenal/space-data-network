import { createRouteBundle } from 'redux-bundler'
import StatusPage from '../status/LoadableStatusPage.js'
import FilesPage from '../files/LoadableFilesPage.js'
import PinsPage from '../pins/LoadablePinsPage.js'
import PeersPage from '../peers/LoadablePeersPage.js'
import SettingsPage from '../settings/LoadableSettingsPage.js'
import AnalyticsPage from '../settings/AnalyticsPage.js'
import WelcomePage from '../welcome/LoadableWelcomePage.js'
import BlankPage from '../blank/BlankPage.js'
import ExplorePageRenderer from '../explore/explore-page-renderer.jsx'
import DiagnosticsPage from '../diagnostics/loadable-diagnostics-page'
import SchemasPage from '../schemas/LoadableSchemasPage.js'
import PluginsPage from '../plugins/LoadablePluginsPage.js'
import TrustPage from '../trust/LoadableTrustPage.js'
import WalletPage from '../wallet/LoadableWalletPage.js'

export default createRouteBundle({
  '/plugins*': PluginsPage,
  '/schemas': SchemasPage,
  '/wallet': WalletPage,
  '/trust*': TrustPage,
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
  '/status*': StatusPage,
  '/': StatusPage,
  '': StatusPage
}, { routeInfoSelector: 'selectHash' })
