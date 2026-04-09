# DWM - Deterministic Working Memory

> Core codename: `Cognitive Anchor`
>
> Slogan: `Anchoring deterministic logic in the chaos of probability.`

## 1. What DWM Is

DWM (Deterministic Working Memory) is an isolated working-memory layer for AI agents.

It is not long-term memory, not part of `SearchMemoryEvents`, and not another profile subsystem.  
Its job is narrow and deliberate: preserve a deterministic, verifiable, overwrite-friendly working state for the current task.

In this repository, DWM is implemented as an isolated scratchpad branch:

- `ScratchpadUpsert`
- `ScratchpadDelete`
- `ScratchpadGet`
- `ScratchpadListKeys`
- `ScratchpadClean`

## 2. What DWM Solves

### 1. Context compaction amnesia

When an agent reads many files, logs, and tool results, the context window eventually gets compacted, truncated, or heavily summarized.  
That destroys the active task line, key files, and execution sequence.

DWM keeps those anchors in structured key/value form before compaction happens.  
After compaction, one `ScratchpadGet` call is enough to restore the active task state.

### 2. Cross-task contamination

When Task B starts immediately after Task A, residual reasoning from Task A can leak into the new workflow.  
DWM prevents that by enforcing a `plan_name` lock: one scope can have only one canonical active plan.

### 3. Format drift

LLMs drift in casing and spelling over long-running tasks.  
DWM uses a “forgive-but-warn” strategy:

- different after case-folding: block the operation,
- same after case-folding but not byte-for-byte identical: allow it and prepend `[FORMAT DRIFT WARNING]`.

### 4. Tool-call I/O bottlenecks

An agent should not need one network round-trip per note.  
DWM supports batch `items[]` upserts so multiple deterministic anchors can be persisted in one atomic write.

## 3. Current Boundary

The scratchpad branch is intentionally isolated from the main durable-memory system:

- it does not enter `memory_nodes`,
- it is not searchable through `SearchMemoryEvents`,
- it does not create `vmm_sessions`,
- it does not depend on the main session lifecycle,
- it does not use recycle trash,
- and it does not provide recovery flows.

It is addressed only by:

- `project_id`
- `user_id`
- `session_id`

Important distinction:

- the external API field is `session_id`,
- the database column is `session_key`,
- and that string key is only used for scratchpad isolation.

## 4. Data Model

The implementation uses two tables.

### 1. `vmm_scratchpad_plans`

Responsibilities:

- lock the single canonical `plan_name` under one `project_id + user_id + session_key`,
- store the last activity timestamp of the whole scratchpad scope.

Key columns:

- `id`
- `project_id`
- `user_id`
- `session_key`
- `plan_name`
- `plan_name_norm`
- `created_timestamp`
- `updated_timestamp`

Uniqueness:

- `(project_id, user_id, session_key)`

### 2. `vmm_scratchpad_nodes`

Responsibilities:

- store deterministic `key/value` anchors under one `plan_id`

Key columns:

- `id`
- `plan_id`
- `item_key`
- `item_value`
- `created_timestamp`
- `updated_timestamp`

Uniqueness:

- `(plan_id, item_key)`

## 5. Final RPC Semantics

### 1. `ScratchpadUpsert`

Purpose:

- insert or overwrite deterministic task anchors

Accepted input styles:

- single item: `key + value`
- batch: `items[]`

Hard rule:

- if `items[]` is non-empty, `key/value` must not be provided
- mixed payloads fail validation
- the batch is atomic
- if any item is invalid, the whole batch fails

Response fields:

- `status`
- `msg`
- `affected_count`
- `inserted_count`
- `updated_count`

### 2. `ScratchpadDelete`

Purpose:

- delete one or more keys

Accepted input styles:

- single key: `key`
- batch: `keys[]`

Hard rule:

- if `keys[]` is non-empty, `key` must not be provided
- mixed payloads fail validation
- the batch is atomic
- delete on an empty scope never fabricates a plan lock

Empty-scope result:

- `status = SUCCESS`
- `msg = "No scratchpad plan exists for the current session. Create records first."`

Response fields:

- `status`
- `msg`
- `affected_count`

### 3. `ScratchpadGet`

Purpose:

- load the whole scratchpad
- or load one filtered anchor batch by `keys[]`

Read rules:

- no `keys[]` or an empty list: return all items
- with `keys[]`: return matching hits or an empty list

No-data behavior:

- not an error
- `status = SUCCESS`
- `items = []`
- `msg = "No scratchpad records found for the current session."`

Returned metadata:

- `plan_name`
- `item_count`
- `updated_timestamp`

Important guarantee:

- `plan_name` is always the canonical stored value
- if no plan exists, `plan_name` is empty

### 4. `ScratchpadListKeys`

Purpose:

- load the current scratchpad plan name plus the full key catalog
- let hosts rebuild the available anchor set without fetching any values

Read rules:

- no `plan_name` is required
- only the normal scope is required:
  - `session_id`
  - `user_id`
  - `project_id`

No-data behavior:

- not an error
- `status = SUCCESS`
- `keys = []`
- `msg = "No scratchpad records found for the current session."`

Returned metadata:

- `plan_name`
- `key_count`
- `updated_timestamp`

Important guarantee:

- `ListKeys` returns the full ordered `keys[]`
- `ListKeys` never returns any `value`
- if no plan exists, `plan_name` is empty

### 5. `ScratchpadClean`

Purpose:

- explicitly terminate the current task scratchpad and delete everything in scope

Behavior:

- delete all matching `nodes`
- then delete the parent `plan`
- empty scope still returns success

Success messages:

- `Scratchpad history has been cleared.`
- or `The current scratchpad is already empty.`

## 6. Plan Guard and Format Drift

The current plan-guard behavior is:

1. If the scope has no plan:
   - `Upsert` creates the first plan lock
   - `Delete` does not create any plan lock
2. If `ToLower(stored_plan_name) != ToLower(input_plan_name)`:
   - block the operation
   - return English guidance telling the caller to check spelling or call `Clean`
3. If the names match after case-folding but differ in raw characters:
   - allow the operation
   - prepend `[FORMAT DRIFT WARNING]`
   - always anchor the warning on the canonical stored plan name

## 7. Background Expiry

Scratchpad data does not enter recycle trash and has no recovery path.  
Expiry is a hard delete.

Rule:

- when `vmm_scratchpad_plans.updated_timestamp` is older than “now minus 15 days”
- the maintenance worker deletes matching `vmm_scratchpad_nodes`
- then deletes matching `vmm_scratchpad_plans`

This pass reuses the existing shared half-hour maintenance clock. It does not run on a separate ticker.

## 8. Relationship with the Main System

Scratchpad participates only in:

- `DeleteProject`
- `DeleteUser`
- 15-day expiry hard delete

It explicitly does not participate in:

- `MigrateProject`
- long-term memory recall
- profile generation
- recycle trash / soft backup
- vector indexing

## 9. Recommended Host Integration

Recommended flow:

1. establish the current `plan_name` at task start,
2. every 3-5 tool calls, batch-write the latest anchors via `ScratchpadUpsert`,
3. when the plan evolves, update the same plan scope instead of spawning a new one,
4. after context compaction, call `ScratchpadGet`,
5. re-inject the returned `items[]` into the host prompt,
6. call `ScratchpadClean` at task end.

Host-side recommendations:

- translate the `ScratchpadStatus` enum into model-friendly text before exposing it to an AI agent,
- do not store full source files inside `value`,
- use `value` for concise execution-relevant summaries, constraints, key file references, and step state.

## 10. Non-goals

DWM is currently not:

- a long-term memory system,
- an automatic task planner,
- a recovery console,
- part of project migration,
- or a generic blob store for large arbitrary text.
