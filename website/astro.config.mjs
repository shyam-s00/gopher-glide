// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import sitemap from '@astrojs/sitemap';
import mermaid from 'astro-mermaid';
import { unified } from '@astrojs/markdown-remark';

// https://astro.build/config
export default defineConfig({
	site: 'https://gopherglide.dev',
	markdown: {
		processor: unified()
	},
	integrations: [
		mermaid(),
		sitemap(),
		starlight({
			title: 'Gopher Glide',
			description: 'HTTP traffic and regression simulator for any backend: Node, Python, Java, .NET, Rust, Go and more. Reuse your .http files, run from your IDE or CI.',
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
				{ label: 'Works With Any Backend', slug: 'any-backend' },
				{ label: 'Load Profiles', slug: 'profiles' },
				{ label: 'Benchmarks', slug: 'benchmarks' },
				{ label: 'The TUI', slug: 'tui' },
				{ label: 'Snapshots (gg snap)', slug: 'snap' },
				{ label: 'IDE Plugins', slug: 'plugin' },
				{ label: 'Driving a Run Live', slug: 'live-control' },
				{ label: 'Live Control Protocol', slug: 'control-protocol' },
				{ label: 'Configuration', slug: 'configuration' },
				{ label: 'CI & Containers', slug: 'ci' },
				{ label: 'CLI Reference', slug: 'cli-reference' },
			],
		}),
	],
});
