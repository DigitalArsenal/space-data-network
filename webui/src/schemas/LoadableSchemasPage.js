import React from 'react'
import Loadable from '@loadable/component'
import ComponentLoader from '../loader/ComponentLoader.js'

const LoadableSchemasPage = Loadable(() => import('./SchemasPage.js'),
  { fallback: <ComponentLoader/> }
)

export default LoadableSchemasPage
