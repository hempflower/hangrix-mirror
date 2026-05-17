#!/usr/bin/env node
//
// build-embed-binaries.mjs orchestrates the two-stage embed chain that
// produces the artefacts hangrix the server `//go:embed`s:
//
//   1. For each linux arch we ship: cross-compile `hangrix-agent`,
//      stage it under apps/hangrix-runner/internal/agentbin/payload/
//      hangrix-agent, then cross-compile `hangrix-runner` for the
//      SAME arch (so the runner ships with the matching agent
//      embedded inside it).
//   2. Drop every runner variant into apps/hangrix/internal/modules/
//      runner/binaries/payload/ with the arch-suffixed filename
//      `hangrix-runner_<goos>_<goarch>` so the next `go build` of
//      apps/hangrix embeds them all.
//
// After this script the server's payload dir looks like:
//
//   payload/.keep
//   payload/.gitignore
//   payload/hangrix-runner_linux_amd64
//   payload/hangrix-runner_linux_arm64
//
// and every embedded runner already has its matching agent inside it.
// The server itself ships no `hangrix-agent` anymore — that path went
// away when the runner started carrying its own agent.
//
// Targets are linux-only on purpose. hangrix-agent only ever runs
// inside a docker container; cross-compiling for darwin/windows would
// just produce binaries that fail to exec on startup.

import { execSync } from "node:child_process";
import { mkdirSync, existsSync, rmSync, readdirSync } from "node:fs";
import { join, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, "../../..");

const agentEmbedDir = resolve(
  repoRoot,
  "apps/hangrix-runner/internal/agentbin/payload",
);
const serverEmbedDir = resolve(
  here,
  "../internal/modules/runner/binaries/payload",
);

// Linux is the only OS we support. Both arches because Apple-silicon
// Macs and AWS Graviton hosts have made linux/arm64 common.
const platforms = [
  { goos: "linux", goarch: "amd64" },
  { goos: "linux", goarch: "arm64" },
];

mkdirSync(agentEmbedDir, { recursive: true });
mkdirSync(serverEmbedDir, { recursive: true });

// Wipe any previously-staged runner binaries so a stale arch from a
// rebuild that dropped support can't leak into the next embed. Also
// covers legacy single-arch `hangrix-agent` / `hangrix-runner` files
// from the pre-multi-arch layout.
for (const entry of readdirSync(serverEmbedDir)) {
  if (
    entry.startsWith("hangrix-runner_") ||
    entry === "hangrix-runner" ||
    entry === "hangrix-agent"
  ) {
    rmSync(join(serverEmbedDir, entry), { force: true });
  }
}

function build({ name, cwd, pkg, out, env }) {
  console.log(`building ${name} (${env.GOOS}/${env.GOARCH}) -> ${out}`);
  execSync(`go build -trimpath -o "${out}" ${pkg}`, {
    cwd,
    stdio: "inherit",
    env: { ...process.env, ...env, CGO_ENABLED: "0" },
  });
  if (!existsSync(out)) {
    throw new Error(`build produced no output at ${out}`);
  }
}

for (const { goos, goarch } of platforms) {
  // Stage the matching-arch agent into the runner's embed dir so the
  // runner build that follows picks it up via //go:embed.
  const agentOut = join(agentEmbedDir, "hangrix-agent");
  build({
    name: "hangrix-agent",
    cwd: join(repoRoot, "apps", "hangrix-agent"),
    pkg: "./cmd/hangrix-agent",
    out: agentOut,
    env: { GOOS: goos, GOARCH: goarch },
  });

  // Build the runner with the agent now embedded inside it.
  const runnerAsset = `hangrix-runner_${goos}_${goarch}`;
  const runnerOut = join(serverEmbedDir, runnerAsset);
  build({
    name: runnerAsset,
    cwd: join(repoRoot, "apps", "hangrix-runner"),
    pkg: "./cmd/hangrix-runner",
    out: runnerOut,
    env: { GOOS: goos, GOARCH: goarch },
  });
}

// Clean up the staged agent: leaving it in the working tree would
// silently get embedded into any future local `go build` of the
// runner — fine functionally but surprising. Drop it.
const stagedAgent = join(agentEmbedDir, "hangrix-agent");
if (existsSync(stagedAgent)) {
  rmSync(stagedAgent, { force: true });
}

console.log("embed-binaries: done");
