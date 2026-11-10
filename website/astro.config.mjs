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
				{
					label: 'Start here',
					items: [
						{ label: 'Getting Started', slug: 'getting-started' },
						{ label: 'Works With Any Backend', slug: 'any-backend' },
						{ label: 'The TUI', slug: 'tui' },
					],
				},
				{
					label: 'Run & shape traffic',
					items: [
						{ label: 'Load Profiles', slug: 'profiles' },
						{ label: 'Driving a Run Live', slug: 'live-control' },
						{ label: 'Configuration', slug: 'configuration' },
					],
				},
				{
					label: 'Catch regressions',
					items: [
						{ label: 'Snapshots (gg snap)', slug: 'snap' },
						{ label: 'CI & Containers', slug: 'ci' },
					],
				},
				{
					label: 'Integrations',
					items: [
						{ label: 'IDE Plugins', slug: 'plugin' },
						{ label: 'AI Agents (MCP)', slug: 'mcp' },
					],
				},
				{
					label: 'Reference',
					collapsed: true,
					items: [
						{ label: 'CLI Reference', slug: 'cli-reference' },
						{ label: 'MCP Tool Reference', slug: 'mcp-tools' },
						{ label: 'Live Control Protocol', slug: 'control-protocol' },
					],
				},
			],
		}),
	],
});
