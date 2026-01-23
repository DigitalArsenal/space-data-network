import { defineConfig } from 'vitepress'

export default defineConfig({
  title: 'Space Data Network',
  description: 'Decentralized peer-to-peer network for exchanging standardized space data',

  // Force dark mode only
  appearance: 'dark',

  head: [
    ['link', { rel: 'icon', href: '/favicon.ico' }],
    ['meta', { name: 'theme-color', content: '#000000' }],
    ['meta', { property: 'og:type', content: 'website' }],
    ['meta', { property: 'og:title', content: 'Space Data Network' }],
    ['meta', { property: 'og:description', content: 'Decentralized P2P network for space data exchange' }],
  ],

  themeConfig: {
    logo: '/logo.svg',

    nav: [
      { text: 'Guide', link: '/guide/getting-started' },
      { text: 'Security', link: '/guide/security-encryption' },
      { text: 'Reference', link: '/reference/schemas' },
      { text: 'API', link: '/api/server' },
      { text: 'Downloads', link: '/downloads' },
      {
        text: 'Links',
        items: [
          { text: 'GitHub', link: 'https://github.com/DigitalArsenal/go-space-data-network' },
          { text: 'npm Package', link: 'https://www.npmjs.com/package/@spacedatanetwork/sdn-js' },
          { text: 'Space Data Standards', link: 'https://spacedatastandards.org' },
        ]
      }
    ],

    sidebar: {
      '/guide/': [
        {
          text: 'Introduction',
          items: [
            { text: 'What is SDN?', link: '/guide/what-is-sdn' },
            { text: 'Getting Started', link: '/guide/getting-started' },
            { text: 'Architecture', link: '/guide/architecture' },
          ]
        },
        {
          text: 'Server',
          items: [
            { text: 'Full Node Setup', link: '/guide/full-node' },
            { text: 'Edge Relay', link: '/guide/edge-relay' },
            { text: 'Configuration', link: '/guide/configuration' },
            { text: 'Deployment', link: '/guide/deployment' },
          ]
        },
        {
          text: 'JavaScript SDK',
          items: [
            { text: 'Installation', link: '/guide/js-installation' },
            { text: 'Browser Usage', link: '/guide/js-browser' },
            { text: 'Node.js Usage', link: '/guide/js-node' },
            { text: 'Data Operations', link: '/guide/js-data' },
          ]
        },
        {
          text: 'Data Ingestion',
          items: [
            { text: 'Pipeline Overview', link: '/guide/ingestion-overview' },
            { text: 'WASM Plugins', link: '/guide/ingestion-plugins' },
            { text: 'Custom Converters', link: '/guide/ingestion-custom' },
          ]
        },
        {
          text: 'Security',
          items: [
            { text: 'Transport Encryption', link: '/guide/security-encryption' },
            { text: 'Encryption at Rest', link: '/guide/encryption-at-rest' },
            { text: 'Schemas & Versioning', link: '/guide/schemas-versioning' },
            { text: 'Digital Identity', link: '/guide/digital-identity' },
          ]
        }
      ],
      '/reference/': [
        {
          text: 'Standards',
          items: [
            { text: 'Schema Overview', link: '/reference/schemas' },
            { text: 'Orbital Data', link: '/reference/orbital' },
            { text: 'Conjunction Data', link: '/reference/conjunction' },
            { text: 'Entity Data', link: '/reference/entity' },
            { text: 'Tracking Data', link: '/reference/tracking' },
          ]
        },
        {
          text: 'Protocols',
          items: [
            { text: 'SDS Exchange', link: '/reference/protocol-sds' },
            { text: 'ID Exchange', link: '/reference/protocol-id' },
            { text: 'PubSub Topics', link: '/reference/pubsub' },
          ]
        }
      ],
      '/api/': [
        {
          text: 'Server API',
          items: [
            { text: 'CLI Reference', link: '/api/server' },
            { text: 'RPC Interface', link: '/api/rpc' },
            { text: 'HTTP API', link: '/api/http' },
          ]
        },
        {
          text: 'JavaScript API',
          items: [
            { text: 'SDNNode', link: '/api/js-node' },
            { text: 'Storage', link: '/api/js-storage' },
            { text: 'Schemas', link: '/api/js-schemas' },
            { text: 'Crypto', link: '/api/js-crypto' },
          ]
        }
      ]
    },

    socialLinks: [
      { icon: 'github', link: 'https://github.com/DigitalArsenal/go-space-data-network' }
    ],

    footer: {
      message: 'Released under the MIT License.',
      copyright: 'Copyright © 2024 Digital Arsenal / Space Data Standards'
    },

    search: {
      provider: 'local'
    },

    editLink: {
      pattern: 'https://github.com/DigitalArsenal/go-space-data-network/edit/main/docs/:path',
      text: 'Edit this page on GitHub'
    }
  }
})
