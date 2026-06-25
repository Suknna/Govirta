# Govirta Project Initialization Implementation Plan

> **Status:** Executed by commits `ac84d8e` and `84e8cf8` on 2026-06-25. Checkboxes preserve the original implementation plan and are not current pending tasks.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Initialize Govirta as a public Go-based GitHub project with explicit project documentation, Apache-2.0 licensing, a fixed Go module path, and an evidence-backed technical analysis framework for the single-node libvirt MVP and long-term orchestration direction.

**Architecture:** The initialization is document-first and fail-closed. It creates no service entrypoint, no web framework, no frontend scaffold, and no libvirt implementation code; it only establishes identity, collaboration rules, module metadata, technical-analysis evidence, version control, and the public remote.

**Tech Stack:** Git, GitHub CLI (`gh`), Go modules, Markdown, Apache-2.0 license, official documentation research via `ctx7` when available and web sources when not.

## Global Constraints

- Project name is `Govirta`.
- GitHub remote is the public repository `Suknna/Govirta`.
- Go module path is `github.com/suknna/govirta`.
- License is Apache-2.0.
- If the remote exists and is empty, reuse it; if it exists and is non-empty, stop without overwrite, force-push, merge, or migration.
- MVP scope is a single-node libvirt web management console.
- Long-term scope is a libvirt-oriented orchestration cluster analogous to the Kubernetes-to-Docker relationship, gradually replacing libvirt-owned capabilities over time.
- The old Incus-as-core-runtime positioning is obsolete for the current project direction.
- Do not create `cmd/`, an HTTP server, a daemon, frontend code, database schema, scheduler, node agent, controller loop, or libvirt binding code during initialization.
- All behavior-affecting choices must be explicit; no hidden defaults, compatibility aliases, silent fallbacks, or magic inference.
- Every completion claim must be backed by fresh command output.

---

## File Structure

Create or modify exactly these files:

- Create: `README.md` — public project overview, MVP scope, long-term direction, current non-goals, development status.
- Create: `AGENTS.md` — project-local rules for future agents, including explicit semantics, evidence requirements, and phase boundaries.
- Create: `.gitignore` — Go, editor, OS, local runtime, build, and `.tmp/` exclusions.
- Create: `LICENSE` — Apache License 2.0 text.
- Create: `go.mod` — Go module declaration only.
- Create: `docs/technical-analysis.md` — evidence-backed technical analysis for MVP and long-term architecture.
- Existing: `docs/superpowers/specs/2026-06-25-project-initialization-design.md` — already committed specification; do not rewrite during implementation unless a contradiction is found.
- Create: `docs/superpowers/plans/2026-06-25-project-initialization.md` — this implementation plan.

No hand-written source code file is expected to exceed the project line-count threshold, because initialization creates no Go source files.

---

### Task 1: Create Base Project Files

**Files:**
- Create: `README.md`
- Create: `AGENTS.md`
- Create: `.gitignore`
- Create: `LICENSE`
- Create: `go.mod`

**Interfaces:**
- Consumes: approved design spec at `docs/superpowers/specs/2026-06-25-project-initialization-design.md`
- Produces: repository identity and rules consumed by Task 2 technical analysis and all future implementation plans

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: base initialization files exist, encode the approved project identity, and introduce no service or framework code.

Acceptance evidence:
- `test -f README.md -a -f AGENTS.md -a -f .gitignore -a -f LICENSE -a -f go.mod` exits 0
- `go list -m` prints `github.com/suknna/govirta`
- `test ! -e cmd/govirta/main.go` exits 0

- [ ] **Step 2: Write `go.mod`**

Write exactly:

```go
module github.com/suknna/govirta

go 1.24
```

Rationale: the current host can validate with its installed Go toolchain; if Go 1.24 is unavailable, record the actual toolchain output and adjust only after explicit evidence. Do not infer a different version silently.

- [ ] **Step 3: Write `.gitignore`**

Write exactly:

```gitignore
# OS metadata
.DS_Store
Thumbs.db

# Editors and IDEs
.idea/
.vscode/
*.swp
*.swo
*~

# Local environment files
.env
.env.*
!.env.example

# Go build and test artifacts
/bin/
/build/
/dist/
*.test
*.out
coverage.out
coverage.html

# Local runtime and agent artifacts
.tmp/
*.pid
*.log

# Dependency and tool caches
.gocache/
```

- [ ] **Step 4: Write `README.md`**

Write content with these exact sections and semantics:

```markdown
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
```

- [ ] **Step 5: Write `AGENTS.md`**

Write content with these exact sections and semantics:

```markdown
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
```

- [ ] **Step 6: Write `LICENSE`**

Use the standard Apache License 2.0 text with the project year 2026 and copyright holder `Suknna` in the appendix notice when a notice is needed. The file must start with:

```text
Apache License
Version 2.0, January 2004
http://www.apache.org/licenses/
```

- [ ] **Step 7: Run targeted verification**

Run:

```bash
go list -m
test -f README.md -a -f AGENTS.md -a -f .gitignore -a -f LICENSE -a -f go.mod
test ! -e cmd/govirta/main.go
```

Expected:

- `go list -m` prints `github.com/suknna/govirta`
- both `test` commands exit 0

---

### Task 2: Build Technical Analysis Evidence

**Files:**
- Create: `docs/technical-analysis.md`

**Interfaces:**
- Consumes: `README.md`, `AGENTS.md`, approved MVP and long-term scope
- Produces: a research-backed analysis document used by later architecture and implementation plans

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: create a technical analysis document that distinguishes verified evidence from open design questions and covers both the single-node libvirt MVP and long-term orchestration direction.

Acceptance evidence:
- `test -f docs/technical-analysis.md` exits 0
- `grep -E "https?://" docs/technical-analysis.md` returns official or project source URLs
- the document contains sections for `MVP`, `长期演进`, `参考模型`, `开放问题`, and `后续验证`

- [ ] **Step 2: Check `ctx7` availability for library documentation**

Run:

```bash
command -v ctx7
```

If available, run:

```bash
ctx7 library libvirt "How to manage virtual machines through libvirt APIs and understand connection, domain, storage, and network concepts"
ctx7 library "go libvirt" "How Go applications should call libvirt APIs and handle connections, domains, storage pools, and networks"
```

Then run `ctx7 docs` for the selected library IDs. Record the executed commands and selected library IDs in `docs/technical-analysis.md`.

If unavailable, record this exact statement in the document: `当前环境没有可用的 ctx7 能力，libvirt 与 Go binding 资料降级为官方站点和公开仓库检索。`

- [ ] **Step 3: Dispatch read-only research agents or run direct web research**

Use read-only research only. No subagent should write files.

Research prompt A:

```text
Research official libvirt architecture and API documentation for a Go-based single-node web management MVP. Return source URLs, key concepts for domains/storage/networking/connections, permission or daemon considerations, and risks for wrapping libvirt behind a local API. Do not propose implementation code.
```

Research prompt B:

```text
Research reference systems for virtualization management and orchestration: Kubernetes control loops and kubelet model, Cockpit, Kimchi, oVirt, Proxmox VE, Harvester, and OpenNebula. Return URLs, what each system teaches Govirta, and which ideas should be avoided for a small MVP. Do not propose implementation code.
```

If subagents are not used, run web searches with the same two scopes and capture URLs manually.

- [ ] **Step 4: Write `docs/technical-analysis.md`**

The document must include:

```markdown
# Govirta 技术分析

## 状态说明

[已验证] 本文档记录初始化阶段可确认的技术方向、外部资料来源和后续开放问题。本文档不锁定 Web 框架、数据库、libvirt Go binding 或集群控制面实现。

## MVP：单机 libvirt Web 管理端

[已验证] MVP 聚焦单机 libvirt 管理。核心问题是如何把 libvirt 的连接、domain、storage pool、volume、network、interface、权限和错误状态显式呈现给浏览器用户。

[分析] 初始化阶段不直接选择 libvirt binding，也不创建 API server。后续设计应优先判断是否需要本地 agent 封装 libvirt 权限边界，再决定 Web/API 进程如何与 libvirt 通信。

## 长期演进：类 Kubernetes 编排集群

[已验证] 长期方向是从“管理本机 libvirt”演进到“声明式编排多节点虚拟化资源”。架构分析应关注 API 对象、期望状态、控制循环、节点 agent、调度、状态存储、网络与存储抽象。

[分析] 不应在 MVP 阶段直接实现集群抽象。MVP 只需要避免把未来必然变化的边界写死，例如把 libvirt 调用散落在 Web handler 中。

## 参考模型

### libvirt

必须至少引用 libvirt 官方站点 `https://libvirt.org/`、libvirt 架构文档、API 文档和连接 URI 文档。内容必须说明 connection、domain、storage pool、volume、network、interface、daemon/permission 边界对 MVP 的影响。

### Kubernetes

必须至少引用 Kubernetes 官方文档中关于架构、控制器、节点、kubelet、API 对象或声明式配置的页面。内容必须说明这些模型对 Govirta 长期控制面、节点 agent、期望状态与实际状态协调的启发，并明确 MVP 不实现集群控制面。

### 现有虚拟化管理项目

必须至少引用 Cockpit、Kimchi、oVirt、Proxmox VE、Harvester、OpenNebula 的官方站点、官方文档或公开仓库页面。内容必须分别说明每个项目对 Govirta 的可借鉴点，以及哪些复杂度不应复制到单机 MVP。

## 开放问题

- MVP 是 Web 进程直接连接 libvirt，还是通过本地 agent 连接 libvirt？
- VM、存储、网络是否需要从第一版就建立内部资源模型？
- 长期控制面是否从单机 API server 平滑演进，还是另起集群控制面？
- 哪些 libvirt 能力应长期保留，哪些能力适合逐步替换？

## 后续验证

- 在真实 Linux/libvirt 环境验证连接权限、错误路径和 domain 生命周期。
- 评估 Go binding 的维护状态、API 覆盖、许可证和版本兼容性。
- 对比直接 libvirt API 与本地 agent 封装的安全边界。
```

Before committing, overwrite those reference subsections with actual researched URLs and findings. Keep `[已验证]` only for statements backed by sources or commands.

- [ ] **Step 5: Run targeted verification**

Run:

```bash
test -f docs/technical-analysis.md
grep -E "https?://" docs/technical-analysis.md
grep -E "MVP|长期演进|参考模型|开放问题|后续验证" docs/technical-analysis.md
```

Expected: each command exits 0 and the URL grep includes both official documentation and reference project URLs.

---

### Task 3: Commit Local Initialization Files

**Files:**
- Modify index only: stage files created by Tasks 1 and 2

**Interfaces:**
- Consumes: base files and technical analysis
- Produces: a local commit ready to push to the public remote

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: create a local initialization commit containing only intended initialization files.

Acceptance evidence:
- `git status --short --branch` lists only intended new files before staging
- `git diff --cached --stat` lists only intended files after staging
- `git log --oneline -10` shows the prior design commit and then the new initialization commit after commit

- [ ] **Step 2: Inspect before staging**

Run:

```bash
git status --short --branch
git diff -- README.md AGENTS.md .gitignore LICENSE go.mod docs/technical-analysis.md
```

Expected: changes are limited to initialization files.

- [ ] **Step 3: Stage exact files**

Run:

```bash
git add README.md AGENTS.md .gitignore LICENSE go.mod docs/technical-analysis.md docs/superpowers/plans/2026-06-25-project-initialization.md
```

- [ ] **Step 4: Inspect staged diff**

Run:

```bash
git diff --cached --stat
git diff --cached -- README.md AGENTS.md .gitignore LICENSE go.mod docs/technical-analysis.md docs/superpowers/plans/2026-06-25-project-initialization.md
```

Expected: staged diff contains only planned initialization files and this plan.

- [ ] **Step 5: Commit**

Run:

```bash
git commit -m "chore: initialize Govirta project"
```

- [ ] **Step 6: Verify commit**

Run:

```bash
git status --short --branch
git log --oneline -10
```

Expected: working tree is clean and the latest commit is `chore: initialize Govirta project`.

---

### Task 4: Create or Reuse GitHub Remote Fail-Closed

**Files:**
- Modify: local git remote configuration only
- Remote: `Suknna/Govirta`

**Interfaces:**
- Consumes: local initialized git repository and committed files
- Produces: `origin` remote pointing to the public GitHub repository

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: create or reuse GitHub public repository `Suknna/Govirta` without overwriting a non-empty remote.

Acceptance evidence:
- `gh repo view Suknna/Govirta --json nameWithOwner,visibility,defaultBranchRef,url` succeeds after creation or reuse
- if `defaultBranchRef` is non-null before local push, stop and report non-empty remote
- `git remote -v` points to `Suknna/Govirta`

- [ ] **Step 2: Check GitHub CLI authentication**

Run:

```bash
gh auth status
```

Expected: authenticated account has permission to create or view `Suknna/Govirta`. If not authenticated, stop and report the actual output.

- [ ] **Step 3: Inspect remote repository state**

Run:

```bash
gh repo view Suknna/Govirta --json nameWithOwner,visibility,defaultBranchRef,url
```

Branch logic:

- If command exits non-zero because the repository does not exist, create it in Step 4.
- If command exits zero and `defaultBranchRef` is `null`, reuse it in Step 5.
- If command exits zero and `defaultBranchRef` is not `null`, stop. The remote is non-empty under this plan.

- [ ] **Step 4: Create public repository if missing**

Run only when Step 3 proves the repository is missing:

```bash
gh repo create Suknna/Govirta --public --description "Go-first libvirt web management and orchestration project" --source=. --remote=origin
```

Expected: `origin` remote is added and the repository is public.

- [ ] **Step 5: Reuse empty repository if it exists**

Run only when Step 3 proves the repository exists and `defaultBranchRef` is `null`:

```bash
if git remote get-url origin >/dev/null 2>&1; then
  CURRENT_ORIGIN=$(git remote get-url origin)
  case "$CURRENT_ORIGIN" in
    https://github.com/Suknna/Govirta.git|git@github.com:Suknna/Govirta.git)
      git remote set-url origin https://github.com/Suknna/Govirta.git
      ;;
    *)
      printf 'unexpected origin remote: %s\n' "$CURRENT_ORIGIN" >&2
      exit 2
      ;;
  esac
else
  git remote add origin https://github.com/Suknna/Govirta.git
fi
```

Expected: `git remote -v` shows `origin` pointing to `Suknna/Govirta`; any unexpected existing remote fails closed with an explicit error.

- [ ] **Step 6: Push main branch**

Run:

```bash
git push -u origin main
```

Expected: push succeeds without force flags.

- [ ] **Step 7: Verify remote after push**

Run:

```bash
git remote -v
gh repo view Suknna/Govirta --json nameWithOwner,visibility,defaultBranchRef,url
```

Expected: repository is public, named `Suknna/Govirta`, and default branch points to `main`.

---

### Task 5: Final Verification Report

**Files:**
- No file changes expected

**Interfaces:**
- Consumes: all prior task outputs
- Produces: final evidence summary for the user

- [ ] **Step 1: Run repository verification**

Run:

```bash
git status --short --branch
git log --oneline -10
git remote -v
go list -m
go list ./...
go test ./...
gh repo view Suknna/Govirta --json nameWithOwner,visibility,defaultBranchRef,url
```

Expected:

- status shows clean `main` branch tracking `origin/main` after push
- log includes design and initialization commits
- remote points to `Suknna/Govirta`
- `go list -m` prints `github.com/suknna/govirta`
- `go list ./...` and `go test ./...` results are recorded exactly; if no packages exist and Go exits non-zero, report that as an initialization-stage limitation rather than hiding it
- GitHub view confirms public repository and default branch

- [ ] **Step 2: Verify no premature code exists**

Run:

```bash
test ! -e cmd/govirta/main.go
test ! -d web
test ! -d frontend
test ! -d internal
```

Expected: all commands exit 0.

- [ ] **Step 3: Report completion with evidence only**

Final response must include:

- 修改内容
- 修改原因
- 验证方法 and exact command outcomes
- 官方文档引用 or explicit note that project-internal initialization files do not involve third-party SDK usage, while technical analysis cites source URLs
- Remote repository URL
- Any failed or non-applicable verification commands with actual output
