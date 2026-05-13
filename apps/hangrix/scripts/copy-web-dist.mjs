// Copies the Nuxt static-generated output into internal/web/dist so that the
// Go binary can pick it up via //go:embed at build time.
import { cpSync, existsSync, mkdirSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const src = resolve(here, '../../web/.output/public')
const dst = resolve(here, '../internal/web/dist')

if (!existsSync(src)) {
  console.error(`source not found: ${src}`)
  console.error('run "pnpm --filter web generate" first')
  process.exit(1)
}

mkdirSync(dst, { recursive: true })
for (const entry of readdirSync(dst)) {
  rmSync(join(dst, entry), { recursive: true, force: true })
}
cpSync(src, dst, { recursive: true })
writeFileSync(join(dst, '.gitkeep'), '')
console.log(`copied ${src} -> ${dst}`)
