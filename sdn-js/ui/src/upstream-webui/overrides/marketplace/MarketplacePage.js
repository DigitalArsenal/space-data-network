import React, { useEffect, useMemo, useState } from 'react'

import { loadMarketplaceListingsFromServer } from '../../../../../src/ui/runtime/marketplace-source.js'
import { buildStoreFeed, searchStoreListings } from '../../../../../src/ui/runtime/store-search.js'

const ALL_VALUE = 'all'

function MarketplacePage() {
  const [listings, setListings] = useState([])
  const [status, setStatus] = useState('loading')
  const [error, setError] = useState(null)
  const [search, setSearch] = useState('')
  const [schemaFilter, setSchemaFilter] = useState(ALL_VALUE)
  const [providerFilter, setProviderFilter] = useState(ALL_VALUE)
  const [paymentFilter, setPaymentFilter] = useState(ALL_VALUE)
  const [statusFilter, setStatusFilter] = useState(ALL_VALUE)

  async function refreshMarketplace() {
    setStatus('loading')
    setError(null)
    try {
      const nextListings = await loadMarketplaceListingsFromServer(runtimeBaseUrl())
      setListings(nextListings)
      setStatus('ready')
    } catch (err) {
      setStatus('error')
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  useEffect(() => {
    refreshMarketplace()
  }, [])

  const filterOptions = useMemo(
    () => buildFilterOptions(listings),
    [listings]
  )

  const filteredListings = useMemo(
    () =>
      listings.filter((listing) =>
        listingMatchesFilters(listing, {
          schemaFilter,
          providerFilter,
          paymentFilter,
          statusFilter
        })
      ),
    [listings, schemaFilter, providerFilter, paymentFilter, statusFilter]
  )

  const searchResults = useMemo(
    () => searchStoreListings(filteredListings.filter((listing) => listing.listingKind !== 'data'), search),
    [filteredListings, search]
  )
  const dataListings = useMemo(
    () => filteredListings
      .filter((listing) => listing.listingKind === 'data')
      .filter((listing) => listingMatchesSearch(listing, search)),
    [filteredListings, search]
  )
  const feed = useMemo(
    () => buildStoreFeed(searchResults, search),
    [searchResults, search]
  )

  return (
    <main className='sdn-marketplace-page w-100 ph3 ph4-l pv3' style={pageStyle}>
      <header className='mb3 flex flex-column flex-row-l justify-between-l items-start-l'>
        <div className='pr4-l'>
          <h1 className='f2 f1-l mv0'>Marketplace</h1>
          <p className='mt2 mb0 f4 lh-copy black-70'>
            {status === 'loading' && listings.length === 0
              ? 'Loading marketplace listings...'
              : `${filteredListings.length.toLocaleString()} visible listings, ${(dataListings.length + searchResults.data.length).toLocaleString()} data views`}
          </p>
        </div>
        <button
          type='button'
          className='button-reset ba b--black-20 bg-white br2 pv2 ph3 mt3 mt0-l pointer hover-bg-near-white'
          onClick={refreshMarketplace}
        >
          Refresh
        </button>
      </header>

      {error && (
        <section className='pa3 ba b--red bg-washed-red dark-red br2 mb3'>
          {error}
        </section>
      )}

      <section className='ba b--black-10 br2 bg-white pa3 mb3'>
        <div className='grid' style={filterGridStyle}>
          <label className='db'>
            <span className='db f6 ttu tracked black-60 mb2'>Search</span>
            <input
              aria-label='Search marketplace'
              className='input-reset ba b--black-20 br2 pa2 w-100 bg-white'
              type='search'
              placeholder='Search modules, providers, schemas'
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </label>
          <SelectFilter
            label='Schema'
            ariaLabel='Filter by schema'
            value={schemaFilter}
            options={filterOptions.schemas}
            onChange={setSchemaFilter}
          />
          <SelectFilter
            label='Provider'
            ariaLabel='Filter by provider'
            value={providerFilter}
            options={filterOptions.providers}
            onChange={setProviderFilter}
          />
          <SelectFilter
            label='Payment'
            ariaLabel='Filter by payment'
            value={paymentFilter}
            options={filterOptions.payments}
            onChange={setPaymentFilter}
          />
          <SelectFilter
            label='Status'
            ariaLabel='Filter by publication status'
            value={statusFilter}
            options={filterOptions.statuses}
            onChange={setStatusFilter}
          />
        </div>
      </section>

      <section className='grid' style={contentGridStyle}>
        <section>
          <h2 className='f4 mt0 mb3'>Modules</h2>
          <div className='grid' style={cardGridStyle}>
            {searchResults.plugins.map((result) => (
              <PluginListingCard key={result.key} result={result} />
            ))}
            {searchResults.plugins.length === 0 && (
              <EmptyState text='No module listings match these filters.' />
            )}
          </div>
        </section>

        <section>
          <h2 className='f4 mt0 mb3'>Data</h2>
          <div className='grid' style={cardGridStyle}>
            {dataListings.map((listing) => (
              <DataListingCard key={`${listing.pluginId}@${listing.version}`} listing={listing} />
            ))}
            {searchResults.data.map((result) => (
              <DataStandardCard key={result.key} result={result} />
            ))}
            {dataListings.length === 0 && searchResults.data.length === 0 && (
              <EmptyState text='No data listings match these filters.' />
            )}
          </div>
        </section>
      </section>

      <section className='mt3'>
        <h2 className='f5 mt0 mb2'>{feed.mode === 'search' ? 'Search feed' : 'Popular feed'}</h2>
        <div className='flex flex-wrap'>
          {feed.entries.slice(0, 12).map((entry) => (
            <span key={`${entry.kind}-${entry.key}`} className='dib br2 bg-near-white ba b--black-10 f6 pv1 ph2 mr2 mb2'>
              {feedEntryLabel(entry)}
            </span>
          ))}
        </div>
      </section>
    </main>
  )
}

function SelectFilter({ label, ariaLabel, value, options, onChange }) {
  return (
    <label className='db'>
      <span className='db f6 ttu tracked black-60 mb2'>{label}</span>
      <select
        aria-label={ariaLabel}
        className='input-reset ba b--black-20 br2 pa2 w-100 bg-white'
        value={value}
        onChange={(event) => onChange(event.target.value)}
      >
        <option value={ALL_VALUE}>All</option>
        {options.map((option) => (
          <option key={option} value={option}>
            {option}
          </option>
        ))}
      </select>
    </label>
  )
}

function PluginListingCard({ result }) {
  const listing = result.listing
  return (
    <article className='ba b--black-10 br2 bg-white pa3' style={cardStyle}>
      <div className='flex items-start justify-between mb2'>
        <div className='pr3'>
          <h3 className='f5 mv0'>{listing.name || listing.pluginId}</h3>
          <div className='f6 black-60 mt1 truncate' title={listing.pluginId}>
            {listing.pluginId} @ {listing.version}
          </div>
        </div>
        <ListingStatus status={listing.status} />
      </div>
      {listing.tagline && <p className='mt2 mb2 black-70'>{listing.tagline}</p>}
      {listing.description && <p className='mt2 mb2 black-70'>{listing.description}</p>}
      <KeyLine label='Provider' value={result.publisherLabel} />
      <KeyLine label='Pricing' value={formatPricing(listing)} />
      <KeyLine label='Payments' value={(listing.acceptedPaymentMethods ?? []).join(', ') || 'not specified'} />
      <KeyLine label='Scope' value={listing.requiredScope || 'not specified'} />
      <ProtectedDeliveryDetails listing={listing} />
      <ChipList values={result.standardsUsed} empty='No SDS schemas advertised.' />
    </article>
  )
}

function DataListingCard({ listing }) {
  return (
    <article className='ba b--black-10 br2 bg-white pa3' style={cardStyle}>
      <div className='flex items-start justify-between mb2'>
        <div className='pr3'>
          <h3 className='f5 mv0'>{listing.name || listing.pluginId}</h3>
          <div className='f6 black-60 mt1 truncate' title={listing.pluginId}>
            {listing.pluginId}
          </div>
        </div>
        <ListingStatus status={listing.status} />
      </div>
      {listing.description && <p className='mt2 mb2 black-70'>{listing.description}</p>}
      <KeyLine label='Provider' value={providerLabel(listing)} />
      <KeyLine label='Pricing' value={formatPricing(listing)} />
      <KeyLine label='Payments' value={(listing.acceptedPaymentMethods ?? []).join(', ') || 'not specified'} />
      <KeyLine label='Access' value={listing.accessType || 'not specified'} />
      <KeyLine label='Sample CID' value={listing.sampleCid || 'not specified'} />
      <ProtectedDeliveryDetails listing={listing} />
      <ChipList values={listing.standardsUsed} empty='No SDS data types advertised.' />
    </article>
  )
}

function ProtectedDeliveryDetails({ listing }) {
  if (!listing.encryptionRequired && !listing.protectedDelivery) {
    return null
  }
  const protectedDelivery = listing.protectedDelivery || {}
  return (
    <div className='mt3 pt2 bt b--black-10'>
      <KeyLine label='Encrypted CID' value={protectedDelivery.encryptedCid || listing.sampleCid || 'not specified'} />
      <KeyLine label='License module' value={protectedDelivery.licenseModuleId || 'licensing/core'} />
      <KeyLine label='Manifest CID' value={protectedDelivery.manifestCid || 'not specified'} />
    </div>
  )
}

function DataStandardCard({ result }) {
  return (
    <article className='ba b--black-10 br2 bg-white pa3' style={cardStyle}>
      <div className='flex items-start justify-between mb2'>
        <h3 className='f5 mv0'>{result.standard}</h3>
        <span className='dib br-pill ba b--light-blue bg-washed-blue blue f7 pv1 ph2'>
          Data
        </span>
      </div>
      <p className='mt2 mb3 black-70'>{result.description}</p>
      <KeyLine label='Modules' value={result.moduleCount.toLocaleString()} />
      <KeyLine label='Providers' value={result.publisherNames.join(', ')} />
      <ChipList values={result.pluginIds} empty='No module listings.' />
    </article>
  )
}

function ListingStatus({ status }) {
  const normalized = status || 'public'
  const className =
    normalized === 'public'
      ? 'b--green bg-washed-green dark-green'
      : normalized === 'retired'
        ? 'b--red bg-washed-red dark-red'
        : 'b--gold bg-washed-yellow brown'
  return (
    <span className={`dib br-pill ba f7 pv1 ph2 ${className}`}>
      {normalized}
    </span>
  )
}

function KeyLine({ label, value }) {
  return (
    <div className='f6 mt2'>
      <span className='black-60'>{label}: </span>
      <span className='black-80'>{value || 'not specified'}</span>
    </div>
  )
}

function ChipList({ values = [], empty }) {
  if (!values.length) {
    return <div className='f6 black-60 mt3'>{empty}</div>
  }
  return (
    <div className='flex flex-wrap mt3'>
      {values.map((value) => (
        <span key={value} className='dib br2 bg-near-white ba b--black-10 f6 pv1 ph2 mr2 mb2'>
          {value}
        </span>
      ))}
    </div>
  )
}

function EmptyState({ text }) {
  return (
    <div className='ba b--black-10 br2 bg-white pa3 black-60'>
      {text}
    </div>
  )
}

function buildFilterOptions(listings) {
  return {
    schemas: uniqueSorted(listings.flatMap((listing) => listing.standardsUsed ?? [])),
    providers: uniqueSorted(listings.map(providerLabel).filter(Boolean)),
    payments: uniqueSorted(listings.map((listing) => listing.paymentModel || 'free')),
    statuses: uniqueSorted(listings.map((listing) => listing.status || 'public'))
  }
}

function listingMatchesFilters(
  listing,
  { schemaFilter, providerFilter, paymentFilter, statusFilter }
) {
  return (
    (schemaFilter === ALL_VALUE || (listing.standardsUsed ?? []).includes(schemaFilter)) &&
    (providerFilter === ALL_VALUE || providerLabel(listing) === providerFilter) &&
    (paymentFilter === ALL_VALUE || (listing.paymentModel || 'free') === paymentFilter) &&
    (statusFilter === ALL_VALUE || (listing.status || 'public') === statusFilter)
  )
}

function listingMatchesSearch(listing, search) {
  const normalized = search.trim().toLowerCase()
  if (!normalized) {
    return true
  }
  return [
    listing.pluginId,
    listing.name,
    listing.description,
    providerLabel(listing),
    listing.accessType,
    ...(listing.tags ?? []),
    ...(listing.standardsUsed ?? [])
  ].filter(Boolean).join(' ').toLowerCase().includes(normalized)
}

function providerLabel(listing) {
  return listing.publisherName || listing.publisherHandle || listing.publisherPeerId || 'Unknown provider'
}

function uniqueSorted(values) {
  return [...new Set(values.map((value) => String(value).trim()).filter(Boolean))]
    .sort((left, right) => left.localeCompare(right))
}

function formatPricing(listing) {
  if ((listing.paymentModel || 'free') === 'free') {
    return 'Free'
  }
  const dollars = Number(listing.priceUsdCents || 0) / 100
  const price = dollars > 0 ? `$${dollars.toFixed(2)}` : 'price not specified'
  if (listing.paymentModel === 'subscription') {
    const days = Number(listing.subscriptionPeriodDays || 0)
    return `${price}${days > 0 ? ` every ${days} days` : ' subscription'}`
  }
  return `${price} one-time`
}

function feedEntryLabel(entry) {
  if (entry.kind === 'plugin') {
    return entry.listing.name || entry.listing.pluginId
  }
  if (entry.kind === 'data') {
    return entry.standard
  }
  return entry.name
}

function runtimeBaseUrl() {
  const configured =
    typeof window !== 'undefined' ? window.__SDN_CONFIG__?.serverBaseUrl : ''
  return String(configured || window.location.origin || '').replace(/\/+$/, '')
}

const pageStyle = {
  minHeight: 'calc(100vh - 1.5rem)'
}

const filterGridStyle = {
  display: 'grid',
  gap: '0.75rem',
  gridTemplateColumns: 'repeat(auto-fit, minmax(12rem, 1fr))'
}

const contentGridStyle = {
  display: 'grid',
  gap: '1rem',
  gridTemplateColumns: 'repeat(auto-fit, minmax(20rem, 1fr))'
}

const cardGridStyle = {
  display: 'grid',
  gap: '0.75rem',
  gridTemplateColumns: 'repeat(auto-fill, minmax(18rem, 1fr))'
}

const cardStyle = {
  minWidth: 0
}

export default MarketplacePage
