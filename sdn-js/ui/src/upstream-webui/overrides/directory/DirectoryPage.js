import React from 'react'

export default function DirectoryPage() {
  return (
    <main className='measure-wide center ph3 ph4-l pv3'>
      <header className='mb3'>
        <h1 className='f2 f1-l mv0'>Directory</h1>
        <p className='mt2 mb0 f4 lh-copy black-70'>
          SDN root directory routing lives here while the upstream IPFS surface stays separate at /webui.
        </p>
      </header>
      <section className='pa3 ba b--black-10 br2 bg-white'>
        <p className='mv0 lh-copy'>
          This page is intentionally minimal until the directory data adapter work lands.
        </p>
      </section>
    </main>
  )
}
