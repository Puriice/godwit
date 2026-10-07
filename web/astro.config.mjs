// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

// https://astro.build/config
export default defineConfig({
	site: 'https://puriice.github.io',
	base: '/godwit',
	integrations: [
		starlight({
			title: 'godwit',
			description: 'A terminal UI and CLI for applying versioned SQL schema migrations to several databases at once.',
			social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/Puriice/godwit' }],
			editLink: { baseUrl: 'https://github.com/Puriice/godwit/edit/main/web/' },
			sidebar: [
				{
					label: 'Guides',
					items: [
						{ slug: 'guides/getting-started' },
						{ slug: 'guides/migration-files' },
						{ slug: 'guides/batch-processing' },
					],
				},
				{
					label: 'Reference',
					items: [
						{ slug: 'reference/commands' },
						{ slug: 'reference/how-migrations-run' },
						{ slug: 'reference/driver-plugins' },
					],
				},
			],
		}),
	],
});
