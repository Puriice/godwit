import { defineConfig } from 'vitepress'

// https://vitepress.dev/reference/site-config
export default defineConfig({
  title: 'godwit',
  description: 'A terminal UI and CLI for applying versioned SQL schema migrations to several databases at once.',
  base: '/godwit/',
  srcDir: 'src',
  cleanUrls: true,
  lastUpdated: true,
  head: [['link', { rel: 'icon', href: '/godwit/favicon.svg' }]],
  themeConfig: {
    nav: [
      { text: 'Guides', link: '/guides/getting-started' },
      { text: 'Reference', link: '/reference/commands' },
    ],
    sidebar: [
      {
        text: 'Guides',
        items: [
          { text: 'Getting started', link: '/guides/getting-started' },
          { text: 'Migration files', link: '/guides/migration-files' },
          { text: 'Batch processing', link: '/guides/batch-processing' },
        ],
      },
      {
        text: 'Reference',
        items: [
          { text: 'Commands', link: '/reference/commands' },
          { text: 'How migrations run', link: '/reference/how-migrations-run' },
          { text: 'Driver plugins', link: '/reference/driver-plugins' },
        ],
      },
    ],
    socialLinks: [{ icon: 'github', link: 'https://github.com/Puriice/godwit' }],
    editLink: {
      pattern: 'https://github.com/Puriice/godwit/edit/main/web/src/:path',
    },
    search: { provider: 'local' },
  },
})
