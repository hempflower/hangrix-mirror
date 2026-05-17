# Minimal agent base image for the smoke. The hangrix runner
# orchestrator bind-mounts /usr/local/bin/hangrix-agent and forces
# `--entrypoint /usr/local/bin/hangrix-agent`, so this image only needs
# to provide the shell + tools the agent's `bash` tool will reach for.
#
# git + curl + ca-certificates cover everything the smoke prompts ask
# for: clone the host repo, edit files, push back, hit the LLM proxy
# over HTTPS-or-HTTP, …
FROM ubuntu:24.04

RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        git curl ca-certificates jq \
 && rm -rf /var/lib/apt/lists/*

# The agent binary writes nothing here at runtime; we just need a
# working directory that exists. The runner bind-mounts /workspace
# onto a fresh host directory at session start.
WORKDIR /workspace
