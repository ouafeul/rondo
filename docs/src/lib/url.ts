// Prefixes the site base, for links written in components and MDX attributes.
export const url = (path: string) => `${import.meta.env.BASE_URL.replace(/\/$/, '')}/${path.replace(/^\//, '')}`;

// Repository files read at build time (CRDs, rondo.yaml.example, platform/). Builds run from docs/.
export const repoFile = (path: string) => new URL(`../${path}`, `file://${process.cwd()}/`);
