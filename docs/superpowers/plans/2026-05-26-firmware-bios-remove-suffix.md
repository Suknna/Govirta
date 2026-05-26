# Firmware BIOS and Case-Insensitive Remove Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a first-class typed QEMU BIOS firmware boundary and make qemu-img qcow2 removal accept `.qcow2` suffixes case-insensitively.

**Architecture:** Introduce `internal/virt/qemu/firmware` as a focused typed renderer package for `-bios`, then route QEMU builders through package-internal typed arguments instead of generic `AddArgument`. Keep `qemuimg.Remove` as local trusted-storage deletion and change only its extension comparison behavior.

**Tech Stack:** Go 1.26, table-driven Go tests, existing Govirta QEMU argv builder and qemu-img remove package.

---

## Tasks

1. Add typed firmware BIOS renderer in `internal/virt/qemu/firmware` with unit tests.
2. Wire `Builder.BIOS(firmware.BIOS)` into QEMU builder and reject generic `-bios`, with QEMU tests and AGENTS updates.
3. Make `qemuimg.Remove` suffix check case-insensitive, with remove tests and AGENTS updates.
4. Run final local verification: `git diff --check`, `go test ./internal/virt/qemu/...`, `go test ./internal/virt/qemuimg/remove`, `scripts/verify.sh`.

Remote `192.168.139.206` CirrOS boot acceptance is out of scope.
