import React, { useState, useMemo } from 'react'
import { Helmet } from 'react-helmet'
import { withTranslation } from 'react-i18next'
import { connect } from 'redux-bundler-react'
import { SCHEMAS, SCHEMA_CATEGORIES } from './schema-data.js'
import SchemaCard from './SchemaCard.js'
import SchemaModal from './SchemaModal.js'
import './SchemasPage.css'

const CATEGORY_KEYS = ['all', ...Object.keys(SCHEMA_CATEGORIES)]

const SchemasPage = ({ t }) => {
  const [search, setSearch] = useState('')
  const [category, setCategory] = useState('all')
  const [viewMode, setViewMode] = useState('grid')
  const [selectedSchema, setSelectedSchema] = useState(null)

  const filtered = useMemo(() => {
    const term = search.toLowerCase()
    return SCHEMAS.filter(s => {
      const matchCat = category === 'all' || s.category === category
      const matchSearch = !term ||
        s.name.toLowerCase().includes(term) ||
        s.fullName.toLowerCase().includes(term) ||
        s.description.toLowerCase().includes(term)
      return matchCat && matchSearch
    })
  }, [search, category])

  return (
    <div className='schemas-page' data-id='SchemasPage'>
      <Helmet>
        <title>{t('Schemas')} | SDN</title>
      </Helmet>
      <div className='schemas-header'>
        <h1>{t('Schemas')}</h1>
        <p className='schemas-header-sub'>Space Data Standards schema registry - {SCHEMAS.length} schemas</p>
      </div>

      <div className='schemas-search'>
        <input
          type='text'
          className='schemas-search-input'
          placeholder='Search schemas by name, description...'
          value={search}
          onChange={e => setSearch(e.target.value)}
        />
        <span className='schemas-count'>{filtered.length} of {SCHEMAS.length}</span>
      </div>

      <div className='schemas-filters'>
        {CATEGORY_KEYS.map(cat => (
          <button
            key={cat}
            type='button'
            className={`schema-filter-btn${category === cat ? ' active' : ''}`}
            onClick={() => setCategory(cat)}
          >
            {cat === 'all' ? 'All' : (SCHEMA_CATEGORIES[cat] ? SCHEMA_CATEGORIES[cat].label : cat)}
          </button>
        ))}
        <div className='schemas-view-toggle'>
          <button
            type='button'
            className={`schema-view-btn${viewMode === 'grid' ? ' active' : ''}`}
            onClick={() => setViewMode('grid')}
          >
            Grid
          </button>
          <button
            type='button'
            className={`schema-view-btn${viewMode === 'list' ? ' active' : ''}`}
            onClick={() => setViewMode('list')}
          >
            List
          </button>
        </div>
      </div>

      {filtered.length === 0
        ? (
          <div className='schemas-empty'>
            <p>No schemas match your search.</p>
          </div>
          )
        : (
          <div className={`schemas-grid${viewMode === 'list' ? ' list-view' : ''}`}>
            {filtered.map(s => (
              <SchemaCard key={s.name} schema={s} onClick={() => setSelectedSchema(s)} />
            ))}
          </div>
          )
      }

      {selectedSchema && (
        <SchemaModal schema={selectedSchema} onClose={() => setSelectedSchema(null)} />
      )}
    </div>
  )
}

export default connect(
  withTranslation()(SchemasPage)
)
