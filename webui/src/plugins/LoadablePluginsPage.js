import React from 'react'
import Loadable from '@loadable/component'
import ComponentLoader from '../loader/ComponentLoader.js'

const LoadablePluginsPage = Loadable(() => import('./PluginsPage.js'),
  { fallback: <ComponentLoader/> }
)

export default LoadablePluginsPage
