# docs/roadmap Knowledge Base

<!--
Verified-against:
  base_commit: 1f893ee
  files:
    - docs/roadmap/README.md
  flows: []
-->

## OVERVIEW

Roadmap maintenance rules only. This directory no longer owns standalone phase-goal documents (the `cycle-*.md` series was removed in the working tree).

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Roadmap maintenance note | `README.md` | Explains that `cycle-*.md` docs are not used |
| Design specs | `../superpowers/specs/` | Store technical design and decisions there |
| Execution plans | `../superpowers/plans/` | Store implementation plans and verification there |

## CONVENTIONS

- Do not add `cycle-*.md` files back to this directory.
- Do not put phase goals in root `README.md` or root `AGENTS.md`.
- Keep this directory limited to roadmap-maintenance guidance; detailed design belongs in specs and execution plans.

## ANTI-PATTERNS

- Do not add any "reconsider libvirt later" language. The roadmap permanently excludes libvirt.
- Do not recreate per-phase checklists or status tables in this directory.

## NOTES

- The permanent architecture direction remains QEMU + QMP + qemu-img + netlink.
- Working tree has deleted `cycle-1` through `cycle-5.md`; the `README.md` rewrite reflects the new structure but is not yet committed (see root `git status`).
