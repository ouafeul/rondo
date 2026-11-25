// Copies the repository CHANGELOG.md into the docs as reference/changelog (run before every build).
import { readFileSync, writeFileSync } from 'node:fs';

const source = new URL('../../CHANGELOG.md', import.meta.url);
const target = new URL('../src/content/docs/reference/changelog.md', import.meta.url);
const body = readFileSync(source, 'utf8').replace(/^# .*\n/, '');
writeFileSync(target, `---\ntitle: Changelog\ndescription: Release history of RONDO.\n---\n${body}`);
