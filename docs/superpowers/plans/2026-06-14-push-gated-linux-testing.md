# Push-Gated Linux Testing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make real Linux/Lima/QEMU tests run automatically only when pushing `main`, while preserving explicit manual script entry points and fast default `go test ./...` feedback.

**Architecture:** Keep Go build-tag isolation unchanged: `acceptance` and `e2e` tests stay outside ordinary `go test ./...`. Simplify `.githooks/pre-push` from path-inferred feature-branch Linux gating to an explicit two-tier gate: every non-delete push runs `scripts/verify.sh`; pushes to `main` additionally run `scripts/acceptance.sh full` and `scripts/e2e.sh full`. Update docs to make the old feature-branch Linux auto-trigger a retired historical strategy.

**Tech Stack:** POSIX shell, Go build constraints, existing Govirta scripts (`scripts/verify.sh`, `scripts/acceptance.sh`, `scripts/e2e.sh`), Markdown docs.

---

## File Structure

- Modify `.githooks/pre-push`: remove feature-branch diff/path inference and `scripts/acceptance.sh linux`; keep deleted-ref skip, `scripts/verify.sh`, and `main` full gate.
- Modify `AGENTS.md`: update acceptance-test notes so only `main` push is automatic; feature branches use explicit manual scripts.
- Modify `docs/superpowers/specs/2026-05-31-lima-acceptance-framework-design.md`: mark its feature-branch Linux path trigger as superseded by `docs/superpowers/specs/2026-06-14-push-gated-linux-testing-design.md`.
- Keep unchanged `scripts/verify.sh`, `scripts/acceptance.sh`, `scripts/e2e.sh`, `test/acceptance/*`, `test/e2e/*`.
- Do not touch existing unrelated worktree changes under `internal/node/controllers/volume_*`.

## Task 1: Simplify Pre-Push Gate

**Files:**
- Modify: `.githooks/pre-push:1-91`
- Reference: `docs/superpowers/specs/2026-06-14-push-gated-linux-testing-design.md:66-73`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `.githooks/pre-push` should run `scripts/verify.sh` for every non-delete push, and should run `scripts/acceptance.sh full` plus `scripts/e2e.sh full` only when the pushed remote ref is `refs/heads/main`.

Acceptance evidence:
- `.githooks/pre-push` contains no `linux_relevant_pattern`.
- `.githooks/pre-push` contains no `scripts/acceptance.sh linux`.
- `.githooks/pre-push` still contains `scripts/verify.sh`.
- `.githooks/pre-push` still contains `scripts/acceptance.sh full` and `scripts/e2e.sh full` inside the `main` gate.

- [ ] **Step 2: Replace inferred Linux gate with explicit main-only mode**

Replace the current `.githooks/pre-push` body with this minimal script:

```sh
#!/bin/sh
set -eu

zero_sha=0000000000000000000000000000000000000000
run_full_gate=0
saw_non_delete=0

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

while read -r local_ref local_sha remote_ref remote_sha; do
	[ -n "${local_ref:-}" ] || continue
	[ "$local_sha" != "$zero_sha" ] || continue
	saw_non_delete=1

	if [ "$remote_ref" = refs/heads/main ]; then
		run_full_gate=1
	fi
done

if [ "$saw_non_delete" -eq 0 ]; then
	printf '%s\n' 'Only deleted refs in this push; skipping verification.'
	exit 0
fi

scripts/verify.sh

if [ "$run_full_gate" -eq 1 ]; then
	# Pushing main is the authority gate: run both the hostnet/egress Lima
	# acceptance suite and the distributed-spine e2e closure. They use
	# distinct Lima instances and LIMA_HOME paths (govirta-acceptance vs
	# govirta-e2e), so running both in one push does not collide.
	scripts/acceptance.sh full
	scripts/e2e.sh full
else
	printf '%s\n' 'No Linux acceptance trigger for this push.'
fi
```

- [ ] **Step 3: Run static hook checks**

Run:

```bash
grep -n 'linux_relevant_pattern\|acceptance\.sh linux' .githooks/pre-push || true
grep -n 'scripts/verify\.sh\|scripts/acceptance\.sh full\|scripts/e2e\.sh full' .githooks/pre-push
```

Expected:
- First command prints no matches.
- Second command prints three matches: `scripts/verify.sh`, `scripts/acceptance.sh full`, `scripts/e2e.sh full`.

- [ ] **Step 4: Run shell syntax check**

Run:

```bash
sh -n .githooks/pre-push
```

Expected: exits 0 with no output.

- [ ] **Step 5: If verification fails, fix the hook only**

Valid failures and fixes:
- Syntax error: fix `.githooks/pre-push` quoting or `if`/`done` structure.
- `acceptance.sh linux` still present: remove stale branch/path-gate code.
- `main` full gate missing: restore `scripts/acceptance.sh full` and `scripts/e2e.sh full` under `run_full_gate=1`.

- [ ] **Step 6: Commit boundary**

Do not commit unless the user explicitly requests a commit. If the user requests it after all tasks pass, stage only `.githooks/pre-push` and use:

```bash
git add .githooks/pre-push
git commit -m "test: gate linux suites on main pushes"
```

## Task 2: Update Testing Policy Docs

**Files:**
- Modify: `AGENTS.md` acceptance-test notes near `COMMANDS` / `ACCEPTANCE TESTS`
- Modify: `docs/superpowers/specs/2026-05-31-lima-acceptance-framework-design.md:43-58`
- Reference: `docs/superpowers/specs/2026-06-14-push-gated-linux-testing-design.md:85-90`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: Documentation should say that default development verification is fast, real Linux/Lima tests run automatically only on `main` push, and manual scripts remain available.

Acceptance evidence:
- `AGENTS.md` no longer says feature branches auto-run Linux acceptance based on path changes.
- `AGENTS.md` still says pushing `main` must pass full Lima acceptance/e2e and must not bypass the hook.
- The 2026-05-31 spec explicitly marks feature-branch path-based acceptance as superseded by the 2026-06-14 design.

- [ ] **Step 2: Update `AGENTS.md` command notes**

In `AGENTS.md`, keep the existing command block unchanged, then update the surrounding acceptance notes to the following meaning:

```markdown
- Fast macOS verification: run `scripts/verify.sh` for the local CI-equivalent loop before broader acceptance; it must remain `gofmt` + ordinary `go test ./...` + main service builds, with no `acceptance` or `e2e` tags.
- Linux/Lima acceptance and distributed E2E are explicit heavy gates: run `scripts/acceptance.sh full` and `scripts/e2e.sh full` manually when validating Linux/QEMU/guest behavior.
- Setup required before pushing: `git config core.hooksPath .githooks`.
- Pushing `main` must pass `scripts/verify.sh`, `scripts/acceptance.sh full`, and `scripts/e2e.sh full`; do not use `git push --no-verify` to bypass the main gate.
- Feature-branch pushes do not infer Linux relevance from changed paths; if a feature needs Linux validation before merging, run the heavy scripts explicitly.
```

- [ ] **Step 3: Mark old feature-branch strategy as superseded**

In `docs/superpowers/specs/2026-05-31-lima-acceptance-framework-design.md`, preserve historical context but mark the old item as superseded. Replace the old goal item that says feature branches run acceptance on Linux-related diffs with:

```markdown
4. 通过本地 `.githooks/pre-push` hook 自动触发：**推 `main` → 无条件全量验收（必过门禁）**；特性分支的 Linux 验收不再由 hook 按 diff 路径隐式推断，已由 `2026-06-14-push-gated-linux-testing-design.md` 改为显式手动入口。
```

- [ ] **Step 4: Run doc consistency checks**

Run:

```bash
grep -n '特性分支.*Linux\|acceptance\.sh linux\|linux_relevant_pattern\|按 diff' AGENTS.md docs/superpowers/specs/2026-05-31-lima-acceptance-framework-design.md docs/superpowers/specs/2026-06-14-push-gated-linux-testing-design.md || true
```

Expected:
- No result should describe feature-branch Linux acceptance as current automatic behavior.
- Results in historical docs must explicitly say the behavior is superseded or no longer automatic.

- [ ] **Step 5: If verification fails, fix wording only**

Valid failures and fixes:
- Current-policy wording still implies path-based feature-branch Linux gate: rewrite to explicit manual validation.
- `acceptance.sh linux` appears as current push behavior: rewrite as old/superseded behavior or remove if redundant.
- Main push gate wording missing: restore `scripts/acceptance.sh full` and `scripts/e2e.sh full` as required main push checks.

- [ ] **Step 6: Commit boundary**

Do not commit unless the user explicitly requests a commit. If the user requests it after all tasks pass, stage only docs changed by this task and use:

```bash
git add AGENTS.md docs/superpowers/specs/2026-05-31-lima-acceptance-framework-design.md docs/superpowers/specs/2026-06-14-push-gated-linux-testing-design.md
git commit -m "docs: document push-gated linux testing"
```

## Task 3: Verify Default Development Path

**Files:**
- Verify: `.githooks/pre-push`
- Verify: `scripts/verify.sh`
- Verify: `docs/superpowers/specs/2026-06-14-push-gated-linux-testing-design.md`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: The default development path should remain fast and must not trigger Linux/Lima/QEMU tests unless explicitly requested or pushing `main`.

Acceptance evidence:
- `scripts/verify.sh` still runs ordinary `go test ./...` without build tags.
- `go test ./...` completes without compiling `acceptance` or `e2e` packages.
- Hook static checks prove `main` still runs heavy gates.

- [ ] **Step 2: Inspect `scripts/verify.sh` for forbidden calls**

Run:

```bash
grep -n -- '-tags acceptance\|-tags e2e\|acceptance\.sh\|e2e\.sh' scripts/verify.sh || true
```

Expected: no output.

- [ ] **Step 3: Run default Go tests**

Run:

```bash
go test ./...
```

Expected:
- PASS for ordinary packages, or a failure only from pre-existing unrelated worktree changes.
- Output must not include `./test/acceptance/...` or `./test/e2e/...` package execution.

If current unrelated `internal/node/controllers/volume_*` worktree changes cause failure, record that as pre-existing and do not fix it as part of this plan.

- [ ] **Step 4: Run hook syntax and policy checks together**

Run:

```bash
sh -n .githooks/pre-push
grep -n 'scripts/acceptance\.sh full\|scripts/e2e\.sh full' .githooks/pre-push
grep -n 'scripts/acceptance\.sh linux\|linux_relevant_pattern' .githooks/pre-push || true
```

Expected:
- `sh -n` exits 0.
- Full-gate grep prints `scripts/acceptance.sh full` and `scripts/e2e.sh full`.
- Linux-path grep prints no matches.

- [ ] **Step 5: If verification fails, classify before changing**

Classification rules:
- Failure caused by the new hook/doc changes: fix this plan's files.
- Failure caused by existing `internal/node/controllers/volume_*` changes: report it as pre-existing and do not change those files.
- Failure caused by a genuine default `go test ./...` compiling `acceptance` or `e2e`: inspect build tags before changing scripts.

- [ ] **Step 6: Final status summary**

Report:
- Modified files.
- Whether ordinary `go test ./...` ran.
- Whether hook checks prove Linux/Lima gates are now `main`-only automatic.
- Whether any verification failure is pre-existing and unrelated.

## Self-Review Checklist

- [x] Spec coverage: covers fast default path, manual heavy scripts, main push gate, removal of feature-branch Linux auto-trigger, docs sync, and verification.
- [x] Placeholder scan: no placeholder red flags remain in implementation steps.
- [x] Type/command consistency: all commands use existing files and modes; no new script modes are invented.
- [x] Scope check: no source-code refactor, no acceptance/e2e test rewrite, no unrelated `internal/node/controllers/volume_*` changes.
