# Govirta Initialization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Initialize Govirta as a compilable Go virtualization infrastructure project skeleton with documentation, Apache-2.0 licensing, AI collaboration rules, three command entrypoints, and minimal internal package boundaries.

**Architecture:** Govirta starts as a single Go module with `cmd/` entrypoints and private platform packages under `internal/`. The skeleton models a Kubernetes-inspired control plane plus compute node architecture while staying independent from Kubernetes/CRD integration. Runtime boundaries are represented by interfaces and no-op implementations for QEMU, QMP, Linux bridge, scheduling, storage, API server, and node agent concerns.

**Tech Stack:** Go 1.26, `github.com/rs/zerolog`, QEMU/QMP/Linux bridge abstractions, Apache-2.0, shell verification script, Conventional Commits.

---

## References

- Spec: `docs/superpowers/specs/2026-05-13-govirta-initialization-design.md`
- Go module reference: https://go.dev/ref/mod
- Go module creation tutorial: https://go.dev/doc/tutorial/create-module
- Go command reference: https://pkg.go.dev/cmd/go
- Apache-2.0 official text: https://www.apache.org/licenses/LICENSE-2.0.txt
- zerolog context API: https://pkg.go.dev/github.com/rs/zerolog
- Conventional Commits: https://www.conventionalcommits.org/

## File Structure Map

Create these files:

```text
.gitignore
AGENTS.md
LICENSE
README.md
go.mod
cmd/govirtad/main.go
cmd/govirtlet/main.go
cmd/govirtctl/main.go
configs/govirtad.example.yaml
configs/govirtlet.example.yaml
docs/architecture.md
scripts/verify.sh
internal/apiserver/server.go
internal/apiserver/server_test.go
internal/controlplane/service.go
internal/controlplane/service_test.go
internal/node/agent.go
internal/node/agent_test.go
internal/scheduler/scheduler.go
internal/store/store.go
internal/virt/qemu/driver.go
internal/virt/qmp/client.go
internal/network/bridge/bridge.go
internal/types/types.go
internal/version/version.go
internal/version/version_test.go
```

Do not modify:

```text
.pomelo_mem/last-update-check.json
docs/superpowers/specs/2026-05-13-govirta-initialization-design.md
```

## Function Call Relationships to Preserve

Planned call chains:

```text
cmd/govirtad/main.go -> internal/controlplane.NewService -> internal/controlplane.Service.Run -> internal/apiserver.Server.Run
cmd/govirtlet/main.go -> internal/node.NewAgent -> internal/node.Agent.Run -> internal/virt/qemu.Driver.Name / internal/virt/qmp.Client.Name / internal/network/bridge.Manager.Name
cmd/govirtctl/main.go -> internal/version.String
scripts/verify.sh -> gofmt check -> go test ./... -> go build ./cmd/govirtad ./cmd/govirtlet ./cmd/govirtctl
```

---

### Task 1: Initialize Git and Go module foundation

**Files:**
- Create: `.gitignore`
- Create: `go.mod`
- Create: `LICENSE`

- [ ] **Step 1: Initialize the Git repository**

Run:

```bash
git init
```

Expected: Git prints that an empty repository was initialized.

- [ ] **Step 2: Create `.gitignore`**

Create `.gitignore` with:

```gitignore
# Go build outputs
/bin/
/dist/
*.test
*.out
coverage.out

# Local environment
.env
.env.*
!.env.example

# Editor and OS files
.DS_Store
.idea/
.vscode/

# Agent/session local state
.pomelo_mem/
.tmp/
```

- [ ] **Step 3: Create `go.mod`**

Create `go.mod` with:

```go.mod
module github.com/suknna/govirta

go 1.26

require github.com/rs/zerolog v1.34.0
```

- [ ] **Step 4: Create `LICENSE`**

Create `LICENSE` using the official Apache License 2.0 text:

```text
                                 Apache License
                           Version 2.0, January 2004
                        http://www.apache.org/licenses/

   TERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION

   1. Definitions.

      "License" shall mean the terms and conditions for use, reproduction,
      and distribution as defined by Sections 1 through 9 of this document.

      "Licensor" shall mean the copyright owner or entity authorized by
      the copyright owner that is granting the License.

      "Legal Entity" shall mean the union of the acting entity and all
      other entities that control, are controlled by, or are under common
      control with that entity. For the purposes of this definition,
      "control" means (i) the power, direct or indirect, to cause the
      direction or management of such entity, whether by contract or
      otherwise, or (ii) ownership of fifty percent (50%) or more of the
      outstanding shares, or (iii) beneficial ownership of such entity.

      "You" (or "Your") shall mean an individual or Legal Entity
      exercising permissions granted by this License.

      "Source" form shall mean the preferred form for making modifications,
      including but not limited to software source code, documentation
      source, and configuration files.

      "Object" form shall mean any form resulting from mechanical
      transformation or translation of a Source form, including but
      not limited to compiled object code, generated documentation,
      and conversions to other media types.

      "Work" shall mean the work of authorship, whether in Source or
      Object form, made available under the License, as indicated by a
      copyright notice that is included in or attached to the work
      (an example is provided in the Appendix below).

      "Derivative Works" shall mean any work, whether in Source or Object
      form, that is based on (or derived from) the Work and for which the
      editorial revisions, annotations, elaborations, or other modifications
      represent, as a whole, an original work of authorship. For the purposes
      of this License, Derivative Works shall not include works that remain
      separable from, or merely link (or bind by name) to the interfaces of,
      the Work and Derivative Works thereof.

      "Contribution" shall mean any work of authorship, including
      the original version of the Work and any modifications or additions
      to that Work or Derivative Works thereof, that is intentionally
      submitted to Licensor for inclusion in the Work by the copyright owner
      or by an individual or Legal Entity authorized to submit on behalf of
      the copyright owner. For the purposes of this definition, "submitted"
      means any form of electronic, verbal, or written communication sent
      to the Licensor or its representatives, including but not limited to
      communication on electronic mailing lists, source code control systems,
      and issue tracking systems that are managed by, or on behalf of, the
      Licensor for the purpose of discussing and improving the Work, but
      excluding communication that is conspicuously marked or otherwise
      designated in writing by the copyright owner as "Not a Contribution."

      "Contributor" shall mean Licensor and any individual or Legal Entity
      on behalf of whom a Contribution has been received by Licensor and
      subsequently incorporated within the Work.

   2. Grant of Copyright License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      copyright license to reproduce, prepare Derivative Works of,
      publicly display, publicly perform, sublicense, and distribute the
      Work and such Derivative Works in Source or Object form.

   3. Grant of Patent License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      (except as stated in this section) patent license to make, have made,
      use, offer to sell, sell, import, and otherwise transfer the Work,
      where such license applies only to those patent claims licensable
      by such Contributor that are necessarily infringed by their
      Contribution(s) alone or by combination of their Contribution(s)
      with the Work to which such Contribution(s) was submitted. If You
      institute patent litigation against any entity (including a
      cross-claim or counterclaim in a lawsuit) alleging that the Work
      or a Contribution incorporated within the Work constitutes direct
      or contributory patent infringement, then any patent licenses
      granted to You under this License for that Work shall terminate
      as of the date such litigation is filed.

   4. Redistribution. You may reproduce and distribute copies of the
      Work or Derivative Works thereof in any medium, with or without
      modifications, and in Source or Object form, provided that You
      meet the following conditions:

      (a) You must give any other recipients of the Work or
          Derivative Works a copy of this License; and

      (b) You must cause any modified files to carry prominent notices
          stating that You changed the files; and

      (c) You must retain, in the Source form of any Derivative Works
          that You distribute, all copyright, patent, trademark, and
          attribution notices from the Source form of the Work,
          excluding those notices that do not pertain to any part of
          the Derivative Works; and

      (d) If the Work includes a "NOTICE" text file as part of its
          distribution, then any Derivative Works that You distribute must
          include a readable copy of the attribution notices contained
          within such NOTICE file, excluding those notices that do not
          pertain to any part of the Derivative Works, in at least one
          of the following places: within a NOTICE text file distributed
          as part of the Derivative Works; within the Source form or
          documentation, if provided along with the Derivative Works; or,
          within a display generated by the Derivative Works, if and
          wherever such third-party notices normally appear. The contents
          of the NOTICE file are for informational purposes only and
          do not modify the License. You may add Your own attribution
          notices within Derivative Works that You distribute, alongside
          or as an addendum to the NOTICE text from the Work, provided
          that such additional attribution notices cannot be construed
          as modifying the License.

      You may add Your own copyright statement to Your modifications and
      may provide additional or different license terms and conditions
      for use, reproduction, or distribution of Your modifications, or
      for any such Derivative Works as a whole, provided Your use,
      reproduction, and distribution of the Work otherwise complies with
      the conditions stated in this License.

   5. Submission of Contributions. Unless You explicitly state otherwise,
      any Contribution intentionally submitted for inclusion in the Work
      by You to the Licensor shall be under the terms and conditions of
      this License, without any additional terms or conditions.
      Notwithstanding the above, nothing herein shall supersede or modify
      the terms of any separate license agreement you may have executed
      with Licensor regarding such Contributions.

   6. Trademarks. This License does not grant permission to use the trade
      names, trademarks, service marks, or product names of the Licensor,
      except as required for reasonable and customary use in describing the
      origin of the Work and reproducing the content of the NOTICE file.

   7. Disclaimer of Warranty. Unless required by applicable law or
      agreed to in writing, Licensor provides the Work (and each
      Contributor provides its Contributions) on an "AS IS" BASIS,
      WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
      implied, including, without limitation, any warranties or conditions
      of TITLE, NON-INFRINGEMENT, MERCHANTABILITY, or FITNESS FOR A
      PARTICULAR PURPOSE. You are solely responsible for determining the
      appropriateness of using or redistributing the Work and assume any
      risks associated with Your exercise of permissions under this License.

   8. Limitation of Liability. In no event and under no legal theory,
      whether in tort (including negligence), contract, or otherwise,
      unless required by applicable law (such as deliberate and grossly
      negligent acts) or agreed to in writing, shall any Contributor be
      liable to You for damages, including any direct, indirect, special,
      incidental, or consequential damages of any character arising as a
      result of this License or out of the use or inability to use the
      Work (including but not limited to damages for loss of goodwill,
      work stoppage, computer failure or malfunction, or any and all
      other commercial damages or losses), even if such Contributor
      has been advised of the possibility of such damages.

   9. Accepting Warranty or Additional Liability. While redistributing
      the Work or Derivative Works thereof, You may choose to offer,
      and charge a fee for, acceptance of support, warranty, indemnity,
      or other liability obligations and/or rights consistent with this
      License. However, in accepting such obligations, You may act only
      on Your own behalf and on Your sole responsibility, not on behalf
      of any other Contributor, and only if You agree to indemnify,
      defend, and hold each Contributor harmless for any liability
      incurred by, or claims asserted against, such Contributor by reason
      of your accepting any such warranty or additional liability.

   END OF TERMS AND CONDITIONS

   APPENDIX: How to apply the Apache License to your work.

      To apply the Apache License to your work, attach the following
      boilerplate notice, with the fields enclosed by brackets "[]"
      replaced with your own identifying information. (Don't include
      the brackets!)  The text should be enclosed in the appropriate
      comment syntax for the file format. We also recommend that a
      file or class name and description of purpose be included on the
      same "printed page" as the copyright notice for easier
      identification within third-party archives.

   Copyright 2026 suknna

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
```

- [ ] **Step 5: Resolve module dependencies**

Run:

```bash
go mod tidy
```

Expected: `go.sum` may be created after code imports zerolog in later tasks. At this step, it may print that no packages matched because no Go packages exist yet.

- [ ] **Step 6: Commit foundation files**

Run:

```bash
git add .gitignore go.mod LICENSE
git commit -m "chore(project): initialize repository foundation"
```

Expected: A commit is created. If Git user identity is not configured, stop and ask the user to configure Git identity rather than changing global Git config.

---

### Task 2: Add project README and AI agent rules

**Files:**
- Create: `README.md`
- Create: `AGENTS.md`

- [ ] **Step 1: Create `README.md`**

Create `README.md` with:

```markdown
# Govirta

Govirta is a Go-based virtualization infrastructure project. It starts from the QEMU layer and builds upward toward a lightweight virtual machine orchestration platform.

The project targets ESXi / VMware-style infrastructure capabilities at the virtualization layer. Its long-term goal is to provide a lighter alternative to OpenStack for virtual machine orchestration scenarios where a full OpenStack deployment is too heavy.

## Architecture Direction

Govirta follows a Kubernetes-inspired architecture without depending on Kubernetes in the short term.

The system is split into:

- **Control plane (`govirtad`)**: owns resource modeling, API boundaries, scheduling, node coordination, and state management.
- **Compute node agent (`govirtlet`)**: runs on virtualization hosts and owns local QEMU, QMP, and Linux bridge integration.
- **CLI (`govirtctl`)**: provides an operator-facing command-line entrypoint.

The intended shape is:

```text
govirtctl -> govirtad control plane -> scheduler/store/control loops -> govirtlet -> QEMU/QMP/Linux bridge
```

## Technology Stack

- Go
- QEMU
- QMP
- Linux bridge
- zerolog for structured logging

## Current Status

Govirta is in an early, fast-iteration development phase. APIs, configuration, package boundaries, and architecture are expected to change substantially. Backward compatibility is not a goal at this stage.

## License

Govirta is licensed under the Apache License 2.0. See [LICENSE](LICENSE).
```

- [ ] **Step 2: Create `AGENTS.md`**

Create `AGENTS.md` with:

```markdown
# Govirta Agent Guide

## Project Context

Govirta is a Go-based virtualization infrastructure platform. It targets ESXi / VMware-style infrastructure capabilities and is expected to become a lightweight virtual machine orchestration alternative to OpenStack for smaller or simpler environments.

Govirta starts from QEMU and builds upward. The compute node wraps QEMU, QMP, and Linux bridge capabilities. The control plane owns resource modeling, scheduling, node coordination, and state management.

The architecture is inspired by Kubernetes control plane / node separation, scheduling, and control-loop ideas, but the project does not target Kubernetes or CRD integration in the short term.

## Current Development Phase

Govirta is in a fast-iteration phase.

- Do not preserve backward compatibility for its own sake.
- Do not keep technical debt only to maintain compatibility with earlier internal APIs, configs, or layouts.
- If an abstraction is wrong, replace or remove it directly.
- Keep changes focused on the current task, but prefer clean replacement over compatibility shims.

## Technology Choices

- Language: Go
- Virtualization layer: QEMU
- QEMU control protocol: QMP
- Host networking boundary: Linux bridge
- Logging: `github.com/rs/zerolog`
- License: Apache-2.0

## Architecture Boundaries

- `govirtad`: control plane process.
- `govirtlet`: compute node agent process.
- `govirtctl`: command-line client.
- `internal/apiserver`: API server boundary.
- `internal/controlplane`: control plane orchestration.
- `internal/scheduler`: VM placement boundary.
- `internal/store`: state storage abstraction.
- `internal/node`: node agent boundary.
- `internal/virt/qemu`: QEMU process abstraction.
- `internal/virt/qmp`: QMP client abstraction.
- `internal/network/bridge`: Linux bridge abstraction.
- `internal/types`: shared domain types.

## Go Context Rules

- The root `context.Context` must be created in `main`.
- Every child context must derive from the root context passed down from `main`.
- Do not create orphan contexts inside internal packages with `context.Background()` or `context.TODO()`.
- Internal packages must accept `ctx context.Context` from their caller for I/O, long-running work, cross-package operations, and goroutines.
- If a timeout or cancellation scope is needed, use `context.WithCancel`, `context.WithTimeout`, or `context.WithDeadline` on the caller-provided context.

## Goroutine Rules

- Every goroutine must have an owner and a shutdown path.
- Every long-running goroutine must select on `ctx.Done()`.
- Do not start fire-and-forget goroutines without error reporting or observability.
- Prefer small runner/worker abstractions over scattered anonymous `go func()` blocks.

## Panic and Recover Rules

- Do not use `panic` for expected business errors.
- Process and goroutine boundaries must recover from panic when a panic could otherwise be lost or crash without context.
- Recover paths must log structured details and convert the panic into an error or shutdown signal.
- Infinite loops are forbidden unless they include `ctx.Done()` or another explicit exit condition.
- Do not use `goto` as normal control flow.

## Error Handling Rules

- Return errors to the caller unless the current layer is explicitly responsible for final handling.
- Wrap errors with `%w` when adding context.
- Use `errors.Is` and `errors.As` for classification.
- Do not match errors by string.
- Do not swallow errors silently.

## Logging Rules

- Use zerolog structured logging.
- Initialize the base logger at the process entrypoint.
- Prefer passing logger context through `context.Context` using zerolog context integration.
- Library packages must not use `fmt.Println` for runtime logs.
- Logs must use stable field names.
- Do not log secrets, tokens, private keys, or sensitive host paths.

## Testing Rules

- Unit tests live next to the package under test.
- Prefer table-driven tests with `t.Run`.
- Test helpers must call `t.Helper()`.
- Use `go test ./...` as the baseline verification command.
- Use `go test -race ./...` for concurrency-sensitive changes.

## Change Reporting Rules

Every implementation handoff must include the key function call relationships affected by the change.

Example:

```text
cmd/govirtlet/main.go -> internal/node.Agent.Run -> internal/virt/qemu.Driver
```

Before changing core logic, inspect and report the affected call chain.

## Git Commit Rules

Use Conventional Commits:

```text
<type>(<scope>): <summary>
```

Examples:

```text
feat(node): add qemu runtime boundary
fix(controlplane): propagate run context cancellation
docs(project): document architecture direction
test(version): cover version string formatting
chore(project): initialize repository foundation
```
```

- [ ] **Step 3: Commit documentation files**

Run:

```bash
git add README.md AGENTS.md
git commit -m "docs(project): describe govirta architecture and agent rules"
```

Expected: A commit is created.

---

### Task 3: Add shared types and version package

**Files:**
- Create: `internal/types/types.go`
- Create: `internal/version/version.go`
- Create: `internal/version/version_test.go`

- [ ] **Step 1: Create `internal/types/types.go`**

Create `internal/types/types.go` with:

```go
package types

// Node represents a compute host managed by Govirta.
type Node struct {
	Name string
}

// VirtualMachine represents a virtual machine managed by Govirta.
type VirtualMachine struct {
	Name string
}

// ResourceList describes coarse compute capacity or demand.
type ResourceList struct {
	CPU    int
	Memory int64
}
```

- [ ] **Step 2: Create `internal/version/version.go`**

Create `internal/version/version.go` with:

```go
package version

const (
	// Name is the project name.
	Name = "govirta"

	// Version is the development version for the initial skeleton.
	Version = "0.1.0-dev"
)

// String returns the human-readable project version.
func String() string {
	return Name + " " + Version
}
```

- [ ] **Step 3: Create `internal/version/version_test.go`**

Create `internal/version/version_test.go` with:

```go
package version

import "testing"

func TestString(t *testing.T) {
	got := String()
	want := "govirta 0.1.0-dev"

	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 4: Run package test**

Run:

```bash
go test ./internal/version
```

Expected: PASS.

- [ ] **Step 5: Commit shared foundations**

Run:

```bash
git add internal/types/types.go internal/version/version.go internal/version/version_test.go
git commit -m "feat(types): add shared domain and version foundations"
```

Expected: A commit is created.

---

### Task 4: Add runtime boundary packages

**Files:**
- Create: `internal/virt/qemu/driver.go`
- Create: `internal/virt/qmp/client.go`
- Create: `internal/network/bridge/bridge.go`

- [ ] **Step 1: Create `internal/virt/qemu/driver.go`**

Create `internal/virt/qemu/driver.go` with:

```go
package qemu

import "context"

// Driver defines the QEMU process management boundary.
type Driver interface {
	Name() string
	Start(ctx context.Context, vmName string) error
	Stop(ctx context.Context, vmName string) error
}

// NoopDriver is a non-operational QEMU driver for the initial skeleton.
type NoopDriver struct{}

// NewNoopDriver creates a QEMU driver that does not execute host commands.
func NewNoopDriver() *NoopDriver {
	return &NoopDriver{}
}

// Name returns the driver name.
func (d *NoopDriver) Name() string {
	return "qemu-noop"
}

// Start validates context cancellation and performs no host operation.
func (d *NoopDriver) Start(ctx context.Context, vmName string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// Stop validates context cancellation and performs no host operation.
func (d *NoopDriver) Stop(ctx context.Context, vmName string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
```

- [ ] **Step 2: Create `internal/virt/qmp/client.go`**

Create `internal/virt/qmp/client.go` with:

```go
package qmp

import "context"

// Client defines the QMP protocol boundary.
type Client interface {
	Name() string
	Connect(ctx context.Context, socketPath string) error
}

// NoopClient is a non-operational QMP client for the initial skeleton.
type NoopClient struct{}

// NewNoopClient creates a QMP client that does not open sockets.
func NewNoopClient() *NoopClient {
	return &NoopClient{}
}

// Name returns the client name.
func (c *NoopClient) Name() string {
	return "qmp-noop"
}

// Connect validates context cancellation and performs no socket operation.
func (c *NoopClient) Connect(ctx context.Context, socketPath string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
```

- [ ] **Step 3: Create `internal/network/bridge/bridge.go`**

Create `internal/network/bridge/bridge.go` with:

```go
package bridge

import "context"

// Manager defines the Linux bridge management boundary.
type Manager interface {
	Name() string
	Ensure(ctx context.Context, bridgeName string) error
}

// NoopManager is a non-operational bridge manager for the initial skeleton.
type NoopManager struct{}

// NewNoopManager creates a bridge manager that does not modify host networking.
func NewNoopManager() *NoopManager {
	return &NoopManager{}
}

// Name returns the manager name.
func (m *NoopManager) Name() string {
	return "bridge-noop"
}

// Ensure validates context cancellation and performs no host networking operation.
func (m *NoopManager) Ensure(ctx context.Context, bridgeName string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
```

- [ ] **Step 4: Run runtime package tests**

Run:

```bash
go test ./internal/virt/qemu ./internal/virt/qmp ./internal/network/bridge
```

Expected: PASS or `? ... [no test files]` for each package.

- [ ] **Step 5: Commit runtime boundaries**

Run:

```bash
git add internal/virt/qemu/driver.go internal/virt/qmp/client.go internal/network/bridge/bridge.go
git commit -m "feat(runtime): add noop qemu qmp and bridge boundaries"
```

Expected: A commit is created.

---

### Task 5: Add control plane, API server, scheduler, and store packages

**Files:**
- Create: `internal/apiserver/server.go`
- Create: `internal/apiserver/server_test.go`
- Create: `internal/scheduler/scheduler.go`
- Create: `internal/store/store.go`
- Create: `internal/controlplane/service.go`
- Create: `internal/controlplane/service_test.go`

- [ ] **Step 1: Create `internal/apiserver/server.go`**

Create `internal/apiserver/server.go` with:

```go
package apiserver

import "context"

// Server defines the control plane API server boundary.
type Server interface {
	Run(ctx context.Context) error
}

// NoopServer is a non-listening API server for the initial skeleton.
type NoopServer struct{}

// NewNoopServer creates an API server that does not listen on a network port.
func NewNoopServer() *NoopServer {
	return &NoopServer{}
}

// Run validates context cancellation and performs no network operation.
func (s *NoopServer) Run(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
```

- [ ] **Step 2: Create `internal/apiserver/server_test.go`**

Create `internal/apiserver/server_test.go` with:

```go
package apiserver

import (
	"context"
	"testing"
)

func TestNoopServerRun(t *testing.T) {
	server := NewNoopServer()

	if err := server.Run(context.Background()); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
}
```

- [ ] **Step 3: Create `internal/scheduler/scheduler.go`**

Create `internal/scheduler/scheduler.go` with:

```go
package scheduler

import (
	"context"

	"github.com/suknna/govirta/internal/types"
)

// Scheduler defines the VM placement boundary.
type Scheduler interface {
	Schedule(ctx context.Context, vm types.VirtualMachine, nodes []types.Node) (types.Node, error)
}

// NoopScheduler returns the first available node without applying policy.
type NoopScheduler struct{}

// NewNoopScheduler creates a scheduler for the initial skeleton.
func NewNoopScheduler() *NoopScheduler {
	return &NoopScheduler{}
}

// Schedule validates context cancellation and returns the first node when present.
func (s *NoopScheduler) Schedule(ctx context.Context, vm types.VirtualMachine, nodes []types.Node) (types.Node, error) {
	select {
	case <-ctx.Done():
		return types.Node{}, ctx.Err()
	default:
	}

	if len(nodes) == 0 {
		return types.Node{}, nil
	}

	return nodes[0], nil
}
```

- [ ] **Step 4: Create `internal/store/store.go`**

Create `internal/store/store.go` with:

```go
package store

import (
	"context"

	"github.com/suknna/govirta/internal/types"
)

// Store defines the state storage boundary.
type Store interface {
	ListNodes(ctx context.Context) ([]types.Node, error)
}

// MemoryStore is a minimal in-memory store for the initial skeleton.
type MemoryStore struct {
	nodes []types.Node
}

// NewMemoryStore creates a store with no initial state.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

// ListNodes returns a copy of known nodes.
func (s *MemoryStore) ListNodes(ctx context.Context) ([]types.Node, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	nodes := make([]types.Node, len(s.nodes))
	copy(nodes, s.nodes)
	return nodes, nil
}
```

- [ ] **Step 5: Create `internal/controlplane/service.go`**

Create `internal/controlplane/service.go` with:

```go
package controlplane

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/apiserver"
)

// Service coordinates control plane components.
type Service struct {
	apiServer apiserver.Server
}

// NewService creates a control plane service with no-op dependencies.
func NewService() *Service {
	return &Service{
		apiServer: apiserver.NewNoopServer(),
	}
}

// Run starts the control plane service.
func (s *Service) Run(ctx context.Context) error {
	zerolog.Ctx(ctx).Info().Str("component", "controlplane").Msg("starting control plane")
	return s.apiServer.Run(ctx)
}
```

- [ ] **Step 6: Create `internal/controlplane/service_test.go`**

Create `internal/controlplane/service_test.go` with:

```go
package controlplane

import (
	"context"
	"io"
	"testing"

	"github.com/rs/zerolog"
)

func TestServiceRun(t *testing.T) {
	logger := zerolog.New(io.Discard)
	ctx := logger.WithContext(context.Background())

	service := NewService()
	if err := service.Run(ctx); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
}
```

- [ ] **Step 7: Run control plane package tests**

Run:

```bash
go test ./internal/apiserver ./internal/controlplane ./internal/scheduler ./internal/store
```

Expected: PASS or `? ... [no test files]` for packages without tests.

- [ ] **Step 8: Commit control plane packages**

Run:

```bash
git add internal/apiserver internal/controlplane internal/scheduler internal/store
git commit -m "feat(controlplane): add api scheduler and store boundaries"
```

Expected: A commit is created.

---

### Task 6: Add node agent package

**Files:**
- Create: `internal/node/agent.go`
- Create: `internal/node/agent_test.go`

- [ ] **Step 1: Create `internal/node/agent.go`**

Create `internal/node/agent.go` with:

```go
package node

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/network/bridge"
	"github.com/suknna/govirta/internal/virt/qemu"
	"github.com/suknna/govirta/internal/virt/qmp"
)

// Agent coordinates compute-node local virtualization dependencies.
type Agent struct {
	qemuDriver    qemu.Driver
	qmpClient     qmp.Client
	bridgeManager bridge.Manager
}

// NewAgent creates a node agent with no-op dependencies.
func NewAgent() *Agent {
	return &Agent{
		qemuDriver:    qemu.NewNoopDriver(),
		qmpClient:     qmp.NewNoopClient(),
		bridgeManager: bridge.NewNoopManager(),
	}
}

// Run starts the node agent skeleton.
func (a *Agent) Run(ctx context.Context) error {
	logger := zerolog.Ctx(ctx).With().
		Str("component", "node").
		Str("qemu_driver", a.qemuDriver.Name()).
		Str("qmp_client", a.qmpClient.Name()).
		Str("bridge_manager", a.bridgeManager.Name()).
		Logger()

	ctx = logger.WithContext(ctx)
	zerolog.Ctx(ctx).Info().Msg("starting node agent")

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
```

- [ ] **Step 2: Create `internal/node/agent_test.go`**

Create `internal/node/agent_test.go` with:

```go
package node

import (
	"context"
	"io"
	"testing"

	"github.com/rs/zerolog"
)

func TestAgentRun(t *testing.T) {
	logger := zerolog.New(io.Discard)
	ctx := logger.WithContext(context.Background())

	agent := NewAgent()
	if err := agent.Run(ctx); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
}
```

- [ ] **Step 3: Run node package test**

Run:

```bash
go test ./internal/node
```

Expected: PASS.

- [ ] **Step 4: Commit node package**

Run:

```bash
git add internal/node
git commit -m "feat(node): add compute agent skeleton"
```

Expected: A commit is created.

---

### Task 7: Add command entrypoints

**Files:**
- Create: `cmd/govirtad/main.go`
- Create: `cmd/govirtlet/main.go`
- Create: `cmd/govirtctl/main.go`

- [ ] **Step 1: Create `cmd/govirtad/main.go`**

Create `cmd/govirtad/main.go` with:

```go
package main

import (
	"context"
	"os"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/controlplane"
)

func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Str("process", "govirtad").Logger()
	ctx := logger.WithContext(context.Background())

	if err := controlplane.NewService().Run(ctx); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("control plane exited with error")
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Create `cmd/govirtlet/main.go`**

Create `cmd/govirtlet/main.go` with:

```go
package main

import (
	"context"
	"os"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/node"
)

func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Str("process", "govirtlet").Logger()
	ctx := logger.WithContext(context.Background())

	if err := node.NewAgent().Run(ctx); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("node agent exited with error")
		os.Exit(1)
	}
}
```

- [ ] **Step 3: Create `cmd/govirtctl/main.go`**

Create `cmd/govirtctl/main.go` with:

```go
package main

import (
	"fmt"

	"github.com/suknna/govirta/internal/version"
)

func main() {
	fmt.Println(version.String())
}
```

- [ ] **Step 4: Run command builds**

Run:

```bash
go build ./cmd/govirtad ./cmd/govirtlet ./cmd/govirtctl
```

Expected: PASS with no output.

- [ ] **Step 5: Commit command entrypoints**

Run:

```bash
git add cmd/govirtad/main.go cmd/govirtlet/main.go cmd/govirtctl/main.go
git commit -m "feat(cmd): add govirta process entrypoints"
```

Expected: A commit is created.

---

### Task 8: Add config examples and architecture document

**Files:**
- Create: `configs/govirtad.example.yaml`
- Create: `configs/govirtlet.example.yaml`
- Create: `docs/architecture.md`

- [ ] **Step 1: Create `configs/govirtad.example.yaml`**

Create `configs/govirtad.example.yaml` with:

```yaml
server:
  bindAddress: 127.0.0.1
  port: 8080

store:
  type: memory
```

- [ ] **Step 2: Create `configs/govirtlet.example.yaml`**

Create `configs/govirtlet.example.yaml` with:

```yaml
node:
  name: local-dev-node

qemu:
  binary: qemu-system-x86_64

qmp:
  socketDir: /var/run/govirta/qmp

network:
  bridge: govirta0
```

- [ ] **Step 3: Create `docs/architecture.md`**

Create `docs/architecture.md` with:

```markdown
# Govirta Architecture

Govirta is a lightweight virtual machine orchestration platform that starts from QEMU and builds upward.

## Architectural Inspiration

Govirta borrows architectural ideas from Kubernetes:

- separate control plane and node agent responsibilities;
- model infrastructure resources explicitly;
- reconcile desired state through future control loops;
- schedule workloads onto nodes through a scheduler boundary.

Govirta does not depend on Kubernetes or CRDs in the short term.

## Process Model

```text
govirtctl -> govirtad -> scheduler/store/control loops -> govirtlet -> QEMU/QMP/Linux bridge
```

## Components

- `govirtad`: control plane process.
- `govirtlet`: compute node process.
- `govirtctl`: CLI process.
- `internal/apiserver`: API boundary.
- `internal/controlplane`: control plane composition.
- `internal/scheduler`: placement boundary.
- `internal/store`: state boundary.
- `internal/node`: node agent composition.
- `internal/virt/qemu`: QEMU process abstraction.
- `internal/virt/qmp`: QMP protocol abstraction.
- `internal/network/bridge`: Linux bridge abstraction.

## Fast Iteration Policy

Govirta is currently in a fast-iteration phase. Backward compatibility is not a goal. Incorrect abstractions should be replaced instead of preserved behind compatibility shims.
```

- [ ] **Step 4: Commit config and architecture docs**

Run:

```bash
git add configs/govirtad.example.yaml configs/govirtlet.example.yaml docs/architecture.md
git commit -m "docs(architecture): add config examples and architecture overview"
```

Expected: A commit is created.

---

### Task 9: Add verification script and finalize dependencies

**Files:**
- Create: `scripts/verify.sh`
- Modify: `go.sum`
- Modify: `go.mod`

- [ ] **Step 1: Create `scripts/verify.sh`**

Create `scripts/verify.sh` with:

```sh
#!/bin/sh
set -eu

unformatted="$(gofmt -l .)"
if [ -n "$unformatted" ]; then
  printf 'gofmt required for:\n%s\n' "$unformatted" >&2
  exit 1
fi

go test ./...
go build ./cmd/govirtad ./cmd/govirtlet ./cmd/govirtctl
```

- [ ] **Step 2: Make the script executable**

Run:

```bash
chmod +x scripts/verify.sh
```

Expected: no output.

- [ ] **Step 3: Format all Go files**

Run:

```bash
gofmt -w cmd internal
```

Expected: no output.

- [ ] **Step 4: Resolve dependencies**

Run:

```bash
go mod tidy
```

Expected: `go.sum` is created or updated with zerolog and transitive dependency checksums.

- [ ] **Step 5: Run full verification**

Run:

```bash
./scripts/verify.sh
```

Expected: `go test ./...` package results appear and all commands exit 0.

- [ ] **Step 6: Commit verification script and module sums**

Run:

```bash
git add scripts/verify.sh go.mod go.sum
git commit -m "chore(project): add verification script and module sums"
```

Expected: A commit is created.

---

### Task 10: Final repository verification and status report

**Files:**
- Verify: all files from this plan

- [ ] **Step 1: Run final tests**

Run:

```bash
go test ./...
```

Expected: PASS for all packages.

- [ ] **Step 2: Run final build verification**

Run:

```bash
go build ./cmd/govirtad ./cmd/govirtlet ./cmd/govirtctl
```

Expected: PASS with no output.

- [ ] **Step 3: Run verification script**

Run:

```bash
./scripts/verify.sh
```

Expected: PASS with exit code 0.

- [ ] **Step 4: Inspect Git status**

Run:

```bash
git status --short
```

Expected: no uncommitted tracked project files except intentionally untracked local files if any. `.pomelo_mem/` should be ignored.

- [ ] **Step 5: Prepare final handoff**

Report these call relationships:

```text
cmd/govirtad/main.go -> internal/controlplane.NewService -> internal/controlplane.Service.Run -> internal/apiserver.Server.Run
cmd/govirtlet/main.go -> internal/node.NewAgent -> internal/node.Agent.Run -> internal/virt/qemu.Driver.Name / internal/virt/qmp.Client.Name / internal/network/bridge.Manager.Name
cmd/govirtctl/main.go -> internal/version.String
```

Report these verification commands and results:

```text
go test ./...
go build ./cmd/govirtad ./cmd/govirtlet ./cmd/govirtctl
./scripts/verify.sh
git status --short
```

---

## Self-Review

- Spec coverage: The plan covers Git initialization, Go module, Apache-2.0, zerolog, three commands, internal package boundaries, README, AGENTS.md, architecture docs, configs, verification script, and final validation.
- Placeholder scan: No `TBD`, `TODO`, "implement later", or unspecified code steps remain.
- Type consistency: `controlplane.NewService`, `Service.Run`, `node.NewAgent`, `Agent.Run`, `apiserver.Server.Run`, runtime no-op constructors, and `version.String` are defined before use by command entrypoints.
- Known execution risk: Git commits may fail if local Git identity is not configured. The executor must not set Git config automatically; ask the user if that happens.
- Known dependency risk: `go mod tidy` requires access to `github.com/rs/zerolog` unless already present in module cache.
