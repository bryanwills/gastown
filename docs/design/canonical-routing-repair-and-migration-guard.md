# Canonical Routing Repair And Migration Guard

Status: design slice for `gt-rca-canon-routing-repair-design` and canonical source `gt-rca-epic-routing.11`.

## Problem

Gas Town can enter a split-brain state where a prefix-routed read such as `bd show <id>` finds a bead, but operational commands such as `gt sling`, `gt hook`, `gt mq`, or `gt doctor` resolve a different beads database. The common causes are stale redirects, malformed or shadowing `routes.jsonl` files, prefix drift between registry/config/database metadata, and environment variables pinning `bd` to a stale database.

The repair must preserve the current safety property: an explicit target rig must fail closed before hook, molecule, polecat, or merge-queue side effects if the bead is not present in the target rig database.

## Scope

This design defines routing invariants, a workspace repair model, and regression tests. The implementation in this slice is intentionally diagnostic-first: doctor reports malformed route configuration before auto-repair. Runtime routing behavior is not broadened and explicit target-rig guards remain authoritative.

Non-goals:

- Do not make `gt sling <bead> <rig>` fall back to the town or source database when the explicit target rig does not contain the bead.
- Do not migrate live beads between Dolt databases as a side effect of normal command execution.
- Do not treat worktree `.beads` directories as source-of-truth databases.

## Current Topology

The town root owns global routing at `.beads/routes.jsonl`. Rig source checkouts live at `<rig>/mayor/rig`. Polecat, crew, and refinery worktrees have `.beads/redirect` files pointing back to the canonical rig beads directory.

For the gastown rig, the canonical tuple is:

| Source | Canonical value |
| --- | --- |
| Town route | `gt- -> gastown/mayor/rig` |
| Rig registry | `mayor/rigs.json` entry `gastown.beads.prefix = gt` |
| Rig beads | `gastown/mayor/rig/.beads` |
| Beads config | `prefix: gt`, `issue-prefix: gt`, `routing.mode: explicit` |
| Dolt metadata | database `gastown` |
| Worktrees | `.beads/redirect` to canonical rig beads |

HQ prefixes remain town-owned:

| Prefix | Owner | Route |
| --- | --- | --- |
| `hq-` | town | `.` |
| `hq-cv-` | town | `.` |

## Invariants

1. Town-level `.beads/routes.jsonl` is the routing source of truth. Rig-level `.beads/routes.jsonl` files are invalid because they can shadow town routes.
2. Rig issue prefixes map to a canonical rig beads directory, normally `<rig>/mayor/rig`, not to a redirect-dependent rig root when the canonical directory exists.
3. `hq-` and `hq-cv-` route to the town root. Non-town prefixes must not route to `.`.
4. Route paths are relative to the town root, clean, and must not contain traversal or absolute paths.
5. Prefixes end in `-` and are unique. Longer town prefixes such as `hq-cv-` must remain valid alongside `hq-`.
6. Worktree `.beads` state is repairable workspace state, not a persistent source of issue truth.
7. `BEADS_DIR`, `BEADS_DOLT_SERVER_DATABASE`, and related target selectors must be stripped or pinned by command helpers so ambient shell state cannot redirect a command silently.
8. Agent beads are explicit exceptions: agent identity records can live in town beads even when their IDs include rig-looking prefixes. Normal work beads still follow prefix routes.
9. Hook state is the work bead state plus `assignee`; legacy `agent.hook_bead` is compatibility data, not authority.
10. Merge-request beads must live in the target rig database so that the rig refinery can list and process them.

## Safety Model

Explicit target-rig commands keep a two-part guard:

1. Routed lookup proves the bead exists somewhere and can be read.
2. Direct target-rig lookup proves the bead exists in the requested target rig before any side effects.

If those disagree, the command fails closed. This preserves cross-rig and HQ safety. HQ/town coordination beads may be tracked or referenced, but they must not silently spawn rig polecats or submit rig merge requests unless a command explicitly creates target-rig-owned work artifacts.

## Workspace Repair

Workspace repair is separate from code changes.

Read-only audit first:

```bash
gt doctor --verbose
gt dolt status
gt dolt migrate --dry-run
gt hooks diff
```

Repair second:

```bash
gt doctor --fix --no-start
gt repair
```

Repair rules:

- Doctor may add missing `hq-` and `hq-cv-` routes.
- Doctor may add missing rig routes when the canonical rig beads directory exists.
- Doctor may rewrite redirect-dependent rig-root routes to canonical `<rig>/mayor/rig` paths when the target has a real `.beads` directory.
- Doctor must report malformed route definitions and refuse auto-fix until they are repaired manually, because regenerating a partially malformed route file can drop operator intent.
- Migration between Dolt databases must be explicit, dry-run-capable, and outside normal command execution.

## Migration Guard

The diagnostic guard for `routes.jsonl` reports:

- malformed JSONL lines;
- empty prefixes or paths;
- prefixes not ending in `-`;
- absolute paths;
- paths that escape the town root;
- non-clean paths such as `rig/./mayor/rig`;
- duplicate prefixes;
- duplicate non-town route paths;
- non-town prefixes mapping to `.`.

This guard catches drift before `LoadRoutes` can silently skip malformed entries and before `gt doctor --fix` can rewrite a damaged file.

## Command Agreement Tests

The regression suite should prove agreement across commands with representative rig and HQ beads.

| Command | Required assertion |
| --- | --- |
| `bd show <rig-id>` | From a polecat worktree with stale `BEADS_*` env, the bead resolves through `routes.jsonl` to canonical rig beads. |
| `bd show <hq-id>` | HQ prefixes resolve to town beads and do not get pinned to the current rig. |
| `gt sling <rig-id> <rig>` | Routed read and direct target-rig read agree before spawn/hook/molecule side effects. |
| `gt sling <hq-id> <rig>` | Fails closed unless the command explicitly creates target-rig-owned work. |
| `gt hook` / `gt hook show` | Hooked work is discovered by work bead status and assignee in the same database that `bd show` uses for the work bead. |
| `gt mq list/status/submit` | MR beads are created/listed in the target rig database, with `rig`, `branch`, `target`, and `source_issue` fields present. |
| `gt doctor` | Route drift, malformed routes, rig-level route files, stale redirects, prefix drift, and database-prefix drift are reported before repair. |

Targeted test packages:

```bash
go test ./internal/beads -run 'Test(GetPrefixForRig|ResolveBeadsDirForID|ValidateRigPrefix|CreateRoutes)'
go test ./internal/cmd -run 'Test(Sling|Hook|Done|MakeTestMR|VerifyBranch)'
go test ./internal/doctor -run 'Test(RoutesCheck|RigRoutesJSONLCheck|PrefixMismatchCheck|DatabasePrefixCheck|BeadsRedirectTargetCheck)'
go test ./internal/refinery -run 'Test(CheckAndCloseCompletedConvoys_UsesHardenedBDEnvs|Engineer_LoadConfig)'
```

## Rollout

1. Land diagnostic route-shape validation and documentation.
2. Run doctor in verbose mode and record current workspace findings as repair work, not source-code changes.
3. Add command-level agreement tests around polluted env, HQ prefixes, explicit target-rig fail-closed behavior, and MR bead location.
4. Only after the diagnostic layer is stable, consider consolidating route resolution helpers for write paths.

This task uses fork PR workflow only: push to `Bella-Giraffety/gastown` and open a PR against `gastownhall/gastown` with base `integration/test-beaddolt-hardenning`. Do not submit this work to the internal merge queue.
