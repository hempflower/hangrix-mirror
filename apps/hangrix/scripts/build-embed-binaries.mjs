#!/usr/bin/env node
//
// build-embed-binaries.mjs cross-compiles `hangrix-agent` and
// `hangrix-runner` into the server's binaries/payload directory so the
// next `go build` of `apps/hangrix` //go:embed-s them into the server
// image. Run via `npm run embed-binaries` (or directly).
//
// Per M6c the runner downloads `hangrix-agent` from the server and
// bind-mounts it into every agent container. The agent binary built
// here is for the OS/arch the server itself is running on; cross-arch
// matrices belong to a future release pipeline, not this script.

import { execSync } from "node:child_process";
import { mkdirSync, copyFileSync, existsSync } from "node:fs";
import { join, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, "../../..");
const payload = resolve(here, "../internal/modules/runner/binaries/payload");
mkdirSync(payload, { recursive: true });

const targets = [
  {
    name: "hangrix-agent",
    cwd: join(repoRoot, "apps", "hangrix-agent"),
    pkg: "./cmd/hangrix-agent",
  },
  {
    name: "hangrix-runner",
    cwd: join(repoRoot, "apps", "hangrix-runner"),
    pkg: "./cmd/hangrix-runner",
  },
];

for (const t of targets) {
  const out = join(payload, t.name);
  console.log(`building ${t.name} → ${out}`);
  // `go build` honours GOOS / GOARCH from env; default = current host.
  execSync(`go build -trimpath -o "${out}" ${t.pkg}`, {
    cwd: t.cwd,
    stdio: "inherit",
  });
  if (!existsSync(out)) {
    throw new Error(`build produced no output at ${out}`);
  }
}
console.log("embed-binaries: done");
