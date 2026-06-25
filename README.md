# Govirta

Govirta is a Go-first virtualization management project.

## Current Goal

The MVP is a single-node web management console for libvirt. The first usable version should make local virtual machine management understandable, explicit, and operable from a browser without hiding libvirt's important operational states.

## Long-Term Direction

Govirta is intended to evolve into a libvirt-oriented orchestration cluster. The long-term relationship should resemble Kubernetes and Docker: Govirta provides declarative APIs, scheduling, reconciliation, and cluster-level operations while libvirt starts as the local execution substrate.

Over time, Govirta may replace capabilities currently owned by libvirt when that replacement creates clearer operational semantics, stronger orchestration behavior, or simpler day-2 operations.

## Current Non-Goals

The initialized repository does not yet provide:

- A runnable web service.
- A CLI entrypoint.
- A frontend application.
- libvirt API calls.
- Cluster scheduling.
- A node agent.
- Persistent state management.

## Engineering Principles

- Prefer explicit behavior over implicit defaults.
- Fail closed when state or intent is ambiguous.
- Avoid compatibility aliases, silent fallbacks, and magic inference.
- Keep the MVP small enough to validate real libvirt workflows before designing cluster abstractions.
- Base technical decisions on official documentation, active open-source references, and reproducible experiments.

## Development Status

This repository is in project initialization and technical analysis phase.
