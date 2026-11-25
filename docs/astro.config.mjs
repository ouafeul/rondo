import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import mermaid from 'astro-mermaid';
import { satteri } from '@astrojs/markdown-satteri';

// Published on GitHub Pages as a project site: https://ouafeul.github.io/rondo/.
const repo = 'https://github.com/ouafeul/rondo';
const base = '/rondo';

// Pages link to each other as `/concepts/model/`; Starlight does not add the
// base to Markdown links, so this prefixes every root-relative URL.
const baseLinks = {
  name: 'rondo-base-links',
  link(node, ctx) {
    const url = node.url;
    if (url.startsWith('/') && !url.startsWith('//') && url !== base && !url.startsWith(`${base}/`)) {
      ctx.setProperty(node, 'url', base + url);
    }
  },
};

export default defineConfig({
  site: 'https://ouafeul.github.io',
  base,
  trailingSlash: 'always',
  markdown: { processor: satteri({ mdastPlugins: [baseLinks] }) },
  // mermaid is one large lazy chunk, loaded only on pages with diagrams
  vite: { build: { chunkSizeWarningLimit: 4000 } },
  integrations: [
    // must precede starlight so ```mermaid blocks are not taken by Expressive Code
    mermaid({
      theme: 'neutral',
      autoTheme: true,
      enableLog: false,
      mermaidConfig: { flowchart: { curve: 'basis', htmlLabels: true } },
    }),
    starlight({
      title: 'RONDO',
      description:
        'Air-gap deployment platform: components (Helm charts, vSphere machines) installed on targets by a Kubernetes operator.',
      logo: { src: './src/assets/logo.svg' },
      favicon: '/favicon.svg',
      defaultLocale: 'root',
      locales: { root: { label: 'English', lang: 'en' } },
      social: [{ icon: 'github', label: 'GitHub', href: repo }],
      editLink: { baseUrl: `${repo}/edit/main/docs/` },
      lastUpdated: false,
      customCss: [
        '@fontsource-variable/inter',
        '@fontsource-variable/jetbrains-mono',
        './src/styles/theme.css',
      ],
      expressiveCode: {
        themes: ['github-dark-default', 'github-light'],
        styleOverrides: { borderRadius: '0.6rem' },
      },
      tableOfContents: { minHeadingLevel: 2, maxHeadingLevel: 3 },
      sidebar: [
        {
          label: 'Getting started',
          items: [{ slug: 'getting-started/first-install' }, { slug: 'getting-started/glossary' }],
        },
        {
          label: 'Concepts',
          items: [
            { slug: 'concepts/model' },
            { slug: 'concepts/architecture' },
            { slug: 'concepts/lifecycle' },
            { slug: 'concepts/values-and-tokens' },
          ],
        },
        {
          label: 'Operate',
          items: [
            { slug: 'operations/prerequisites' },
            { slug: 'operations/artifact-mirrors' },
            { slug: 'operations/environments' },
            { slug: 'operations/day-to-day' },
            { slug: 'operations/troubleshooting' },
          ],
        },
        {
          label: 'Write a component',
          items: [
            { slug: 'components/tutorial' },
            { slug: 'components/chart' },
            { slug: 'components/machines' },
            { slug: 'components/sandbox' },
            { slug: 'components/publish' },
          ],
        },
        {
          label: 'Reference',
          items: [
            { slug: 'reference/cli' },
            { slug: 'reference/rondo-yaml' },
            { slug: 'reference/api' },
            { slug: 'reference/jobs' },
            { slug: 'reference/rondo-system' },
            { slug: 'reference/changelog' },
          ],
        },
        {
          label: 'Contributing',
          items: [{ slug: 'contributing/repository' }, { slug: 'contributing/release' }],
        },
      ],
    }),
  ],
});
