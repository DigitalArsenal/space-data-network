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
  const [selectedListingKey, setSelectedListingKey] = useState(null)

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
  const visibleListings = useMemo(
    () => [
      ...searchResults.plugins.map((result) => result.listing),
      ...dataListings
    ],
    [searchResults.plugins, dataListings]
  )
  const selectedListing = useMemo(
    () => visibleListings.find((listing) => listingKey(listing) === selectedListingKey) || visibleListings[0] || null,
    [selectedListingKey, visibleListings]
  )

  useEffect(() => {
    if (selectedListingKey && !visibleListings.some((listing) => listingKey(listing) === selectedListingKey)) {
      setSelectedListingKey(null)
    }
  }, [selectedListingKey, visibleListings])

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
              <PluginListingCard
                key={result.key}
                result={result}
                selected={selectedListing && listingKey(result.listing) === listingKey(selectedListing)}
                onSelect={() => setSelectedListingKey(listingKey(result.listing))}
              />
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
              <DataListingCard
                key={listingKey(listing)}
                listing={listing}
                selected={selectedListing && listingKey(listing) === listingKey(selectedListing)}
                onSelect={() => setSelectedListingKey(listingKey(listing))}
              />
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

      {selectedListing && (
        <ListingDetailView listing={selectedListing} />
      )}

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

function PluginListingCard({ result, selected, onSelect }) {
  const listing = result.listing
  return (
    <article className={`ba br2 bg-white pa3 ${selected ? 'b--blue shadow-1' : 'b--black-10'}`} style={cardStyle}>
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
      <FieldStreamPolicyDetails listing={listing} />
      <ChipList values={result.standardsUsed} empty='No SDS schemas advertised.' />
      <button
        type='button'
        className='button-reset ba b--blue bg-blue white br2 pv2 ph3 mt3 pointer hover-bg-dark-blue'
        onClick={onSelect}
      >
        View details
      </button>
    </article>
  )
}

function DataListingCard({ listing, selected, onSelect }) {
  return (
    <article className={`ba br2 bg-white pa3 ${selected ? 'b--blue shadow-1' : 'b--black-10'}`} style={cardStyle}>
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
      <FieldStreamPolicyDetails listing={listing} />
      <ChipList values={listing.standardsUsed} empty='No SDS data types advertised.' />
      <button
        type='button'
        className='button-reset ba b--blue bg-blue white br2 pv2 ph3 mt3 pointer hover-bg-dark-blue'
        onClick={onSelect}
      >
        View details
      </button>
    </article>
  )
}

function ListingDetailView({ listing }) {
  const protectedDelivery = listing.protectedDelivery || {}
  const detailRows = [
    ['Provider identity', providerLabel(listing)],
    ['Provider peer ID', listing.publisherPeerId],
    ['Provider EPM CID', listing.providerEpmCid],
    ['Terms', formatTerms(listing)],
    ['Pricing', formatPricing(listing)],
    ['Payment methods', (listing.acceptedPaymentMethods ?? []).join(', ')],
    ['Supported schemas', (listing.standardsUsed ?? []).join(', ')],
    ['Verification state', verificationState(listing)],
    ['Sample CID', listing.sampleCid],
    ['Encrypted CID', protectedDelivery.encryptedCid],
    ['Manifest CID', protectedDelivery.manifestCid],
    ['Content hash', protectedDelivery.contentHash],
    ['License module', protectedDelivery.licenseModuleId || (listing.encryptionRequired ? 'licensing/core' : '')],
    ['Grant scope', listing.requiredScope || protectedDelivery.grantScope],
    ['Delivery protocol', protectedDelivery.deliveryProtocol]
  ]

  return (
    <section className='mt4 pt3 bt b--black-10'>
      <div className='flex flex-column flex-row-l justify-between-l items-start-l mb3'>
        <div className='pr4-l'>
          <h2 className='f3 mt0 mb1'>{listing.name || listing.pluginId}</h2>
          <div className='f6 black-60'>{listing.pluginId} @ {listing.version}</div>
        </div>
        <ListingStatus status={listing.status} />
      </div>
      {listing.description && <p className='measure-wide black-70 mt0 mb3'>{listing.description}</p>}
      <div className='grid' style={detailGridStyle}>
        {detailRows
          .filter(([, value]) => value)
          .map(([label, value]) => (
            <div key={label} className='pv2 bb b--black-10'>
              <div className='f7 ttu tracked black-50'>{label}</div>
              <div className='f6 black-80 mt1 break-word'>{value}</div>
            </div>
          ))}
      </div>
      <ChipList values={listing.tags} empty='No listing tags advertised.' />
      <FieldStreamPolicyDetails listing={listing} />
      <PurchaseAccessPanel listing={listing} />
    </section>
  )
}

function PurchaseAccessPanel({ listing }) {
  const [buyerPeerId, setBuyerPeerId] = useState(defaultBuyerPeerId())
  const [encryptionPubkey, setEncryptionPubkey] = useState(defaultEncryptionPubkey())
  const [tierName, setTierName] = useState('Basic')
  const [paymentMethod, setPaymentMethod] = useState(defaultPaymentMethod(listing))
  const [preferredDeliveryMethod, setPreferredDeliveryMethod] = useState(defaultDeliveryMethod(listing))
  const [status, setStatus] = useState('idle')
  const [error, setError] = useState('')
  const [purchase, setPurchase] = useState(null)
  const [grant, setGrant] = useState(null)
  const [deliveryStatus, setDeliveryStatus] = useState('idle')
  const [deliveryResult, setDeliveryResult] = useState(null)

  async function createPurchase() {
    setStatus('creating')
    setError('')
    setGrant(null)
    setDeliveryStatus('idle')
    setDeliveryResult(null)
    try {
      const payload = {
        listing_id: listing.pluginId,
        tier_name: tierName.trim() || 'Basic',
        buyer_peer_id: buyerPeerId.trim(),
        buyer_encryption_pubkey: encryptionPubkey.trim(),
        key_algorithm: 'x25519',
        payment_method: paymentMethodValue(paymentMethod),
        preferred_delivery_method: preferredDeliveryMethod
      }
      if (!payload.buyer_peer_id) {
        throw new Error('Buyer peer ID is required')
      }
      if (!payload.buyer_encryption_pubkey) {
        throw new Error('Buyer encryption public key is required')
      }
      const response = await fetch(`${runtimeBaseUrl()}/api/storefront/purchases`, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload)
      })
      if (!response.ok) {
        throw new Error(`Purchase request failed (${response.status})`)
      }
      const nextPurchase = await response.json()
      setPurchase(nextPurchase)
      setStatus('purchase-created')
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setStatus('error')
    }
  }

  async function payWithCredits() {
    const requestId = purchase?.request_id || purchase?.requestId
    if (!requestId) {
      return
    }
    setStatus('paying')
    setError('')
    try {
      const response = await fetch(`${runtimeBaseUrl()}/api/storefront/purchases/${encodeURIComponent(requestId)}/pay-credits`, {
        method: 'POST',
        credentials: 'include'
      })
      if (!response.ok) {
        throw new Error(`Credits payment failed (${response.status})`)
      }
      const nextGrant = await response.json()
      setGrant(nextGrant)
      setStatus('grant-issued')
      setDeliveryStatus('ready')
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setStatus('error')
    }
  }

  async function markManualDevPaid() {
    const requestId = purchase?.request_id || purchase?.requestId
    if (!requestId) {
      return
    }
    setStatus('paying')
    setError('')
    try {
      const response = await fetch(`${runtimeBaseUrl()}/api/storefront/purchases/${encodeURIComponent(requestId)}/manual-dev-paid`, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          reference: `browser-fixture-${Date.now()}`,
          note: 'Browser marketplace manual/dev settlement verification'
        })
      })
      if (!response.ok) {
        throw new Error(`Manual/dev settlement failed (${response.status})`)
      }
      const payload = await response.json()
      const nextGrant = payload?.grant || payload
      if (payload?.purchase) {
        setPurchase(payload.purchase)
      }
      setGrant(nextGrant)
      setStatus('grant-issued')
      setDeliveryStatus('ready')
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setStatus('error')
    }
  }

  async function verifyEncryptedDelivery() {
    const encryptedCid = listing.protectedDelivery?.encryptedCid || listing.sampleCid
    const clientDecrypt = marketplaceClientDecrypt()
    setDeliveryStatus('verifying')
    setError('')
    setDeliveryResult(null)
    try {
      if (!grant) {
        throw new Error('Grant is required before encrypted delivery verification')
      }
      if (!encryptedCid) {
        throw new Error('Encrypted CID is required before encrypted delivery verification')
      }
      if (!clientDecrypt || typeof clientDecrypt.decryptArtifact !== 'function') {
        throw new Error('Browser client-decrypt adapter is unavailable')
      }
      const encryptedBundleBytes = await fetchMarketplaceEncryptedBundle(clientDecrypt, {
        cid: encryptedCid,
        listing,
        purchase,
        grant
      })
      const grantResponseBytes = decodeBase64Bytes(
        grant.grant_response_base64 || grant.grantResponseBase64 || ''
      )
      const decryptedBytes = await clientDecrypt.decryptArtifact({
        listing,
        purchase,
        grant,
        grantResponseBytes,
        encryptedBundleBytes
      })
      const loadResult = typeof clientDecrypt.loadModule === 'function'
        ? await clientDecrypt.loadModule({
          listing,
          purchase,
          grant,
          bytes: decryptedBytes
        })
        : null
      setDeliveryResult({
        decryptedBytes: byteLength(decryptedBytes),
        loadLabel: loadResultLabel(loadResult)
      })
      setDeliveryStatus('verified')
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setDeliveryStatus('error')
    }
  }

  return (
    <section className='mt4 pa3 ba b--black-10 br2 bg-near-white'>
      <div className='flex flex-column flex-row-l justify-between-l items-start-l'>
        <div className='pr3-l'>
          <h3 className='f4 mt0 mb1'>Purchase access</h3>
          <div className='f6 black-60'>
            Creates a storefront purchase for this listing and records encrypted delivery state when a grant is issued.
          </div>
        </div>
        <span className='dib br2 bg-white ba b--black-10 f6 pv1 ph2 mt2 mt0-l'>
          {statusLabel(status)}
        </span>
      </div>
      <div className='grid mt3' style={purchaseGridStyle}>
        <label className='db'>
          <span className='db f6 ttu tracked black-60 mb2'>Buyer peer ID</span>
          <input
            aria-label='Buyer peer ID'
            className='input-reset ba b--black-20 br2 pa2 w-100 bg-white'
            value={buyerPeerId}
            onChange={(event) => setBuyerPeerId(event.target.value)}
          />
        </label>
        <label className='db'>
          <span className='db f6 ttu tracked black-60 mb2'>Encryption public key</span>
          <input
            aria-label='Buyer encryption public key'
            className='input-reset ba b--black-20 br2 pa2 w-100 bg-white'
            value={encryptionPubkey}
            onChange={(event) => setEncryptionPubkey(event.target.value)}
          />
        </label>
        <label className='db'>
          <span className='db f6 ttu tracked black-60 mb2'>Tier</span>
          <input
            aria-label='Tier name'
            className='input-reset ba b--black-20 br2 pa2 w-100 bg-white'
            value={tierName}
            onChange={(event) => setTierName(event.target.value)}
          />
        </label>
        <label className='db'>
          <span className='db f6 ttu tracked black-60 mb2'>Payment</span>
          <select
            aria-label='Payment method'
            className='input-reset ba b--black-20 br2 pa2 w-100 bg-white'
            value={paymentMethod}
            onChange={(event) => setPaymentMethod(event.target.value)}
          >
            {paymentOptions(listing).map((method) => (
              <option key={method} value={method}>{method}</option>
            ))}
          </select>
        </label>
        <label className='db'>
          <span className='db f6 ttu tracked black-60 mb2'>Delivery</span>
          <select
            aria-label='Preferred delivery method'
            className='input-reset ba b--black-20 br2 pa2 w-100 bg-white'
            value={preferredDeliveryMethod}
            onChange={(event) => setPreferredDeliveryMethod(event.target.value)}
          >
            {['IPFSPin', 'PubSubStream', 'DirectTransfer', 'WebhookPush'].map((method) => (
              <option key={method} value={method}>{method}</option>
            ))}
          </select>
        </label>
      </div>
      <div className='mt3 flex flex-wrap'>
        <button
          type='button'
          className='button-reset ba b--blue bg-blue white br2 pv2 ph3 mr2 mb2 pointer hover-bg-dark-blue'
          onClick={createPurchase}
          disabled={status === 'creating' || status === 'paying'}
        >
          Create purchase
        </button>
        <button
          type='button'
          className='button-reset ba b--green bg-green white br2 pv2 ph3 mb2 pointer hover-bg-dark-green'
          onClick={payWithCredits}
          disabled={!purchase || status === 'paying'}
        >
          Pay with credits
        </button>
        <button
          type='button'
          className='button-reset ba b--dark-blue bg-white dark-blue br2 pv2 ph3 mb2 ml2-l pointer hover-bg-near-white'
          onClick={markManualDevPaid}
          disabled={!purchase || status === 'paying'}
        >
          Mark manual/dev paid
        </button>
      </div>
      {error && <div className='mt2 dark-red f6'>{error}</div>}
      {purchase && (
        <div className='mt3 pa2 bg-white ba b--black-10 br2'>
          <KeyLine label='Purchase ID' value={purchase.request_id || purchase.requestId} />
          <KeyLine label='Purchase status' value={String(purchase.status ?? 'pending')} />
        </div>
      )}
      {grant && (
        <div className='mt3 pa2 bg-white ba b--black-10 br2'>
          <KeyLine label='Grant ID' value={grant.grant_id || grant.grantId} />
          <KeyLine label='Delivery topic' value={grant.delivery_topic || grant.deliveryTopic} />
          <KeyLine label='Encrypted CID' value={listing.protectedDelivery?.encryptedCid || listing.sampleCid} />
          <button
            type='button'
            className='button-reset ba b--dark-blue bg-white dark-blue br2 pv2 ph3 mt3 pointer hover-bg-near-white'
            onClick={verifyEncryptedDelivery}
            disabled={deliveryStatus === 'verifying'}
          >
            Verify encrypted delivery
          </button>
          {deliveryStatus !== 'idle' && (
            <div className='mt2 f6 black-70'>
              {deliveryStatusLabel(deliveryStatus)}
            </div>
          )}
          {deliveryResult && (
            <div className='mt2 f6 black-80'>
              <div>Decrypted bytes: {deliveryResult.decryptedBytes}</div>
              {deliveryResult.loadLabel && <div>Loaded {deliveryResult.loadLabel}</div>}
            </div>
          )}
        </div>
      )}
    </section>
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

function FieldStreamPolicyDetails({ listing }) {
  const policy = listing.protectedDelivery?.fieldStreamPolicy
  if (!policy) {
    return null
  }
  return (
    <div className='mt3 pt2 bt b--black-10'>
      <div className='f6 b black-70 mb2'>Field-stream policy</div>
      <KeyLine label='Policy ID' value={fieldPolicyValue(policy.policyId || policy.policy_id)} />
      <KeyLine label='Policy version' value={fieldPolicyValue(policy.policyVersion || policy.policy_version)} />
      <KeyLine label='Stream ID' value={fieldPolicyValue(policy.streamId || policy.stream_id)} />
      <KeyLine label='Schema code' value={fieldPolicyValue(policy.schemaCode || policy.schema_code)} />
      <KeyLine label='Key epoch' value={fieldPolicyValue(policy.keyEpoch || policy.key_epoch)} />
      <KeyLine label='Grant scope' value={fieldPolicyValue(policy.grantScope || policy.grant_scope)} />
      <KeyLine label='Allowed fields' value={fieldPolicyList(policy.allowedFieldPaths || policy.allowed_field_paths)} />
      <KeyLine label='Redacted fields' value={fieldPolicyList(policy.redactedFieldPaths || policy.redacted_field_paths)} />
      <KeyLine label='Allowed operations' value={fieldPolicyList(policy.allowedOperations || policy.allowed_operations)} />
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

function fieldPolicyValue(value) {
  if (value === null || value === undefined || value === '') {
    return ''
  }
  return String(value)
}

function fieldPolicyList(values) {
  return Array.isArray(values) && values.length > 0
    ? values.map((value) => String(value).trim()).filter(Boolean).join(', ')
    : ''
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

function formatTerms(listing) {
  const access = listing.accessType || listing.listingKind || 'listing'
  const payment = listing.paymentModel || 'free'
  const period = listing.subscriptionPeriodDays ? `${listing.subscriptionPeriodDays} day term` : ''
  return [access, payment, period].filter(Boolean).join(' / ')
}

function verificationState(listing) {
  if (listing.status === 'retired') {
    return 'Retired by provider'
  }
  if (listing.publisherPeerId && (listing.protectedDelivery?.encryptedCid || listing.sampleCid)) {
    return listing.encryptionRequired ? 'Provider-bound encrypted artifact' : 'Provider-bound listing'
  }
  if (listing.publisherPeerId) {
    return 'Provider identity present'
  }
  return 'Metadata only'
}

function listingKey(listing) {
  return `${listing.listingKind || 'listing'}:${listing.pluginId}:${listing.version}`
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

function defaultBuyerPeerId() {
  if (typeof window === 'undefined') {
    return ''
  }
  return window.__SDN_CONFIG__?.peerId || window.__SDN_CONFIG__?.nodePeerId || ''
}

function defaultEncryptionPubkey() {
  if (typeof window === 'undefined') {
    return ''
  }
  return window.__SDN_CONFIG__?.encryptionPublicKey || ''
}

function paymentOptions(listing) {
  const methods = listing.acceptedPaymentMethods?.length
    ? listing.acceptedPaymentMethods
    : [listing.paymentModel === 'free' ? 'Free' : 'SDN_Credits']
  return uniqueSorted(methods)
}

function defaultPaymentMethod(listing) {
  return paymentOptions(listing)[0] || 'SDN_Credits'
}

function paymentMethodValue(method) {
  switch (String(method || '').toLowerCase()) {
    case 'crypto_eth':
    case 'eth':
      return 0
    case 'crypto_sol':
    case 'sol':
      return 1
    case 'crypto_btc':
    case 'btc':
      return 2
    case 'sdn_credits':
    case 'credits':
      return 3
    case 'fiat_stripe':
    case 'stripe':
      return 4
    case 'free':
      return 5
    default:
      return 3
  }
}

function defaultDeliveryMethod(listing) {
  if (listing.protectedDelivery?.deliveryProtocol) {
    return 'IPFSPin'
  }
  if (listing.accessType === 'streaming' || listing.accessType === 'subscription') {
    return 'PubSubStream'
  }
  return 'IPFSPin'
}

function marketplaceClientDecrypt() {
  if (typeof window === 'undefined') {
    return null
  }
  return window.__SDN_MARKETPLACE_CLIENT_DECRYPT__ || window.__SDN_CLIENT_DECRYPT__ || null
}

async function fetchMarketplaceEncryptedBundle(clientDecrypt, request) {
  if (typeof clientDecrypt.fetchEncryptedBundle === 'function') {
    const bytes = await clientDecrypt.fetchEncryptedBundle(request)
    return normalizeBytes(bytes)
  }
  throw new Error('Encrypted bundle fetch adapter is unavailable')
}

function decodeBase64Bytes(value) {
  if (!value) {
    return new Uint8Array()
  }
  const binary = atob(String(value).replace(/-/g, '+').replace(/_/g, '/'))
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index)
  }
  return bytes
}

function normalizeBytes(value) {
  if (value instanceof Uint8Array) {
    return value
  }
  if (value instanceof ArrayBuffer) {
    return new Uint8Array(value)
  }
  if (ArrayBuffer.isView(value)) {
    return new Uint8Array(value.buffer, value.byteOffset, value.byteLength)
  }
  throw new Error('Encrypted bundle adapter must return bytes')
}

function byteLength(value) {
  if (value instanceof Uint8Array || value instanceof ArrayBuffer || ArrayBuffer.isView(value)) {
    return value.byteLength
  }
  return 0
}

function loadResultLabel(result) {
  if (!result || typeof result !== 'object') {
    return ''
  }
  return result.operation || result.operationName || result.moduleId || ''
}

function deliveryStatusLabel(status) {
  switch (status) {
    case 'ready':
      return 'Encrypted delivery ready'
    case 'verifying':
      return 'Decrypt/load running'
    case 'verified':
      return 'Decrypt/load complete'
    case 'error':
      return 'Decrypt/load blocked'
    default:
      return ''
  }
}

function statusLabel(status) {
  switch (status) {
    case 'creating':
      return 'Creating purchase'
    case 'purchase-created':
      return 'Purchase created'
    case 'paying':
      return 'Completing payment'
    case 'grant-issued':
      return 'Grant issued'
    case 'error':
      return 'Action needed'
    default:
      return 'Ready'
  }
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

const detailGridStyle = {
  display: 'grid',
  gap: '0 1rem',
  gridTemplateColumns: 'repeat(auto-fit, minmax(14rem, 1fr))'
}

const purchaseGridStyle = {
  display: 'grid',
  gap: '0.75rem',
  gridTemplateColumns: 'repeat(auto-fit, minmax(14rem, 1fr))'
}

export default MarketplacePage
