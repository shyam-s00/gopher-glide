// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import mermaid from 'astro-mermaid';
import sitemap from '@astrojs/sitemap';

// https://astro.build/config
export default defineConfig({
	site: 'https://gopherglide.dev',
	integrations: [
		mermaid(),
		sitemap(),
		starlight({
			title: 'Gopher Glide',
			description: 'High-fidelity API traffic simulation from your IDE. Build, test, and break APIs with zero boilerplate.',
			logo: {
				light: './public/assets/ggToolIcon.svg',
				dark: './public/assets/ggToolIcon_dark.svg',
			},
			components: {
				Footer: './src/components/Footer.astro',
				SiteTitle: './src/components/SiteTitle.astro',
				PageTitle: './src/components/PageTitle.astro'
			},
			favicon: '/assets/ggToolIcon_dark.svg',
			customCss: ['./src/styles/custom.css'],
			head: [
				{
					tag: 'meta',
					attrs: { property: 'og:image', content: 'https://gopherglide.dev/assets/og-image.png' },
				},
				{
					tag: 'meta',
					attrs: { name: 'twitter:image', content: 'https://gopherglide.dev/assets/og-image.png' },
				},
				{
					tag: 'meta',
					attrs: { name: 'twitter:card', content: 'summary_large_image' },
				},
			],
			social: [
				{ icon: 'github', label: 'GitHub', href: 'https://github.com/shyam-s00/gopher-glide' }
			],
			sidebar: [
				{ label: 'Home', slug: 'index' },
				{ label: 'Getting Started', slug: 'getting-started' },
				{ label: 'Load Profiles', slug: 'profiles' },
				{ label: 'Benchmarks', slug: 'benchmarks' },
				{ label: 'The TUI', slug: 'tui' },
				{ label: 'Snapshots (gg snap)', slug: 'snap' },
				{ label: 'JetBrains Plugin', slug: 'plugin' },
				{ label: 'Configuration', slug: 'configuration' },
			],
		}),
	],
});
