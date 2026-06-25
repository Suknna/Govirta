# Govirta Agent Guide

## Project Direction

Govirta starts as a single-node libvirt web management console and is expected to evolve into a libvirt-oriented orchestration cluster. The long-term architecture should learn from Kubernetes-style declarative APIs, controllers, node agents, scheduling, and reconciliation loops.

The previous Incus-as-core-runtime positioning is obsolete for the current direction.

## Current Phase

The repository is in initialization and technical analysis phase. Do not create runnable service entrypoints, web frameworks, frontend scaffolds, database schemas, schedulers, node agents, or libvirt binding code until a later implementation plan explicitly requires them.

## Explicit Semantics

Prefer explicit behavior over implicit behavior in API design, migration strategy, admission control, and operational semantics.

Forbidden patterns:

- Hidden defaults that affect behavior.
- Silent fallback between backends or execution modes.
- Compatibility aliases that translate user intent without being declared.
- Magic value inference for runtime behavior.
- Fail-open admission gates when state is incomplete or ambiguous.

Automated inference is acceptable only when the inferred result is visible and editable before it affects runtime behavior.

## Technical Decision Rules

Before adopting third-party libraries, SDKs, frameworks, or major architectural patterns:

1. Check official documentation.
2. Check active open-source references.
3. Record source URLs and version assumptions in the relevant design or analysis document.
4. Prefer the smallest dependency surface that can validate the MVP.

Do not choose a web framework, libvirt Go binding, database, frontend stack, or cluster architecture from memory alone.

## MVP Boundaries

MVP analysis should focus on:

- Single-node libvirt inventory and control.
- Browser-based operational workflows.
- Clear representation of VM state, storage, networking, and host capabilities.
- Explicit error reporting for libvirt connection and permission failures.
- A future path toward API-server and node-agent separation without implementing that separation prematurely.

## Verification Requirements

Do not claim completion without fresh verification evidence. Prefer commands that directly prove the claim, such as:

- `go list -m`
- `go test ./...`
- `git status --short --branch`
- `git remote -v`
- `gh repo view Suknna/Govirta --json nameWithOwner,visibility,defaultBranchRef,url`

If a verification command fails, report the actual output and root cause before changing strategy.
