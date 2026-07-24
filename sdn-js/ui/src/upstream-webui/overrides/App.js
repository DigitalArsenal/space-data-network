import React, { Component } from 'react'
import PropTypes from 'prop-types'
import { connect } from 'redux-bundler-react'
import { getNavHelper } from 'internal-nav-helper'
import ReactJoyride from 'react-joyride'
import { withTranslation } from 'react-i18next'
import { normalizeFiles } from '../../../../../webui/src/lib/files.js'
import { DropTarget } from 'react-dnd'
import { NativeTypes } from 'react-dnd-html5-backend'
import { appTour } from '../../../../../webui/src/lib/tours.js'
import { getJoyrideLocales } from '../../../../../webui/src/helpers/i8n.js'
import NavBar from './navigation/NavBar.js'
import ComponentLoader from '../../../../../webui/src/loader/ComponentLoader.js'
import Notify from '../../../../../webui/src/components/notify/Notify.js'
import Connected from '../../../../../webui/src/components/connected/Connected.js'
import TourHelper from '../../../../../webui/src/components/tour/TourHelper.js'
import FilesExploreForm from '../../../../../webui/src/files/explore-form/files-explore-form.tsx'

export class App extends Component {
  static propTypes = {
    doSetupLocalStorage: PropTypes.func.isRequired,
    doTryInitIpfs: PropTypes.func.isRequired,
    doUpdateUrl: PropTypes.func.isRequired,
    doUpdateHash: PropTypes.func.isRequired,
    doFilesWrite: PropTypes.func.isRequired,
    routeInfo: PropTypes.object.isRequired,
    filesPathInfo: PropTypes.object,
    isOver: PropTypes.bool.isRequired
  }

  constructor(props) {
    super(props)
    props.doSetupLocalStorage()
  }

  componentDidMount() {
    this.props.doTryInitIpfs()
  }

  addFiles = async (filesPromise) => {
    const { doFilesWrite, doUpdateHash, routeInfo, filesPathInfo } = this.props
    const isFilesPage = routeInfo.pattern === '/files*'
    const addAtPath = isFilesPage ? (filesPathInfo?.realPath || routeInfo.params.path) : '/'
    const files = await filesPromise

    doFilesWrite(normalizeFiles(files), addAtPath)
    if (!isFilesPage) {
      doUpdateHash('/files')
    }
  }

  handleJoyrideCb = (data) => {
    if (data.action === 'close') {
      this.props.doDisableTooltip()
    }
  }

  render() {
    const { t, route: Page, ipfsReady, doFilesNavigateTo, routeInfo: { url }, connectDropTarget, canDrop, isOver, showTooltip } = this.props
    const canRenderWithoutIpfs = url === '/welcome' || url.startsWith('/settings') || url === '/modules' || url === '/marketplace'
    return connectDropTarget(
      <div className='sans-serif h-100 relative' onClick={getNavHelper(this.props.doUpdateUrl)}>
        { canDrop && isOver && <div className='h-100 top-0 right-0 fixed appOverlay' style={{ background: 'rgba(99, 202, 210, 0.2)' }} /> }
        <div className='flex flex-row-reverse-l flex-column-reverse justify-end justify-start-l' style={{ minHeight: '100vh' }}>
          <div className='flex-auto-l'>
            <div className='flex items-center ph3 ph4-l' style={{ WebkitAppRegion: 'drag', height: 75, background: '#F0F6FA', paddingTop: '20px', paddingBottom: '15px' }}>
              <div className='joyride-app-explore' style={{ width: 560 }}>
                <FilesExploreForm onBrowse={doFilesNavigateTo} />
              </div>
              <div className='dn flex-ns flex-auto items-center justify-end'>
                {!url.startsWith('/diagnostics') && <TourHelper />}
                <Connected className='joyride-app-status' />
              </div>
            </div>
            <main className='bg-white pv3 pa3 pa4-l'>
              { (ipfsReady || canRenderWithoutIpfs)
                ? <Page />
                : <ComponentLoader />
              }
            </main>
          </div>
          <div className='navbar-container flex-none-l bg-navy'>
            <NavBar />
          </div>
        </div>

        <ReactJoyride
          run={showTooltip}
          steps={appTour.getSteps({ t })}
          styles={appTour.styles}
          callback={this.handleJoyrideCb}
          scrollToFirstStep
          disableOverlay
          locale={getJoyrideLocales(t)}
        />

        <Notify />
      </div>
    )
  }
}

const dropTarget = {
  drop: (props, monitor, App) => {
    if (monitor.didDrop()) {
      return
    }

    const { filesPromise } = monitor.getItem()
    App.addFiles(filesPromise)
  },
  canDrop: (props) => props.filesPathInfo ? props.filesPathInfo.isMfs : true
}

const dropCollect = (connectDnD, monitor) => ({
  connectDropTarget: connectDnD.dropTarget(),
  isOver: monitor.isOver(),
  canDrop: monitor.canDrop()
})

export const AppWithDropTarget = DropTarget(NativeTypes.FILE, dropTarget, dropCollect)(App)

export default connect(
  'selectRoute',
  'selectRouteInfo',
  'selectIpfsReady',
  'selectShowTooltip',
  'doFilesNavigateTo',
  'doUpdateUrl',
  'doUpdateHash',
  'doSetupLocalStorage',
  'doTryInitIpfs',
  'doFilesWrite',
  'doDisableTooltip',
  'selectFilesPathInfo',
  withTranslation('app')(AppWithDropTarget)
)
