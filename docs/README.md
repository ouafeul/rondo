# RONDO documentation

Astro + Starlight, published to GitHub Pages (https://ouafeul.github.io/rondo/) by the `pages.yml` workflow.

```bash
cd docs
npm ci
npm run dev              # http://localhost:4321/rondo/
npm run build            # dist/
```

`src/content/docs/`: the pages (MDX). `src/components/`: diagrams and interactive tools,
`src/data/`: their data. The API reference, the `rondo.yaml` explorer and the base-layer graph
are generated at build time from `manifests/crds`, `rondo.yaml.example` and
`platform/components`.
