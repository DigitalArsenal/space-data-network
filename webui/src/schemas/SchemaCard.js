import React from 'react'
import { SCHEMA_CATEGORIES } from './schema-data.js'

const SchemaCard = ({ schema, onClick }) => {
  const cat = SCHEMA_CATEGORIES[schema.category]
  return (
    <div className='schema-card' onClick={onClick} role='button' tabIndex={0} onKeyDown={e => { if (e.key === 'Enter') onClick() }}>
      <div className='schema-card-name'>{schema.name}</div>
      <div className='schema-card-fullname'>{schema.fullName}</div>
      <div className='schema-card-meta'>
        <span className={`schema-category-badge ${schema.category}`}>
          {cat ? cat.label : schema.category}
        </span>
        <span className='schema-card-fields'>{schema.fields.length} fields</span>
      </div>
    </div>
  )
}

export default SchemaCard
