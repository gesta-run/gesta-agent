# Token Accounting Guardrails

## Purpose

This document defines the token-accounting contract shared by Gesta Agent,
Control, and Console. It records the failure modes found during the August 2026
audit so future changes do not reintroduce token inflation, silent loss, or
incompatible rollout behavior.

Token accounting is a cross-repository protocol change. Any change to token
fields, totals, cursor behavior, or event validation must be reviewed across all
three repositories before release.

## Canonical total

Gesta's product token total includes every disjoint accounting tier:

```text
total_tokens = input_tokens
             + output_tokens
             + cache_read_tokens
             + cache_write_tokens
```

The four stored fields must be disjoint before this formula is applied.

Provider payloads are not necessarily disjoint:

- Codex/OpenAI reports cached input as a subset of input. Agent must normalize
  fresh input to `input_tokens - cached_input_tokens`, then store the cached
  subset in `cache_read_tokens`.
- Claude reports input, cache creation, and cache read as separate tiers. Agent
  must preserve those tiers without subtracting cache again.
- Reasoning-token fields that are already included in output must not be added
  to the total a second time.
- A new provider must document whether every cache field is a subset or a
  separate tier before its usage can enter the canonical total.

Large totals are not evidence of a bug by themselves. Long-running agent
sessions repeatedly read large cached contexts, so cache tokens may dominate
the total by an order of magnitude. Investigations must compare the four tiers
before declaring an inflation incident.

## Authoritative fact and UI consistency

`turn.usage` is the current authoritative fact for dashboards, efficiency,
session totals, Prometheus export, and remote write. All consumers must use the
same canonical total and must not independently reinterpret provider fields.

Breakdowns such as user, model, repository, work type, and day may fan out the
same fact for grouping, but a product total must sum exactly one complete
dimension. It must never add multiple breakdown dimensions together.

Session totals are lifetime totals. Dashboard totals are bounded by their
query window. The UI must label this distinction instead of attempting to make
the two values equal.

## Confirmed failure modes

### Forked Codex sessions can replay parent turns

A Codex fork rollout contains a copied prefix of its parent's rollout. Scanning
a newly discovered fork from offset zero and emitting every completed turn
reports the copied parent turns again under the child session. Server-side
event-ID idempotency cannot remove this duplication because the session ID is
part of the stable event identity.

Required behavior:

1. Read and propagate `forked_from_id` or the equivalent parent-session field.
2. Identify turns inherited from the parent by stable turn ID.
3. Use inherited records to establish the child's cumulative token baseline.
4. Do not emit `turn.usage` for inherited turns.
5. Begin emission at the first child-unique turn.
6. Persist enough state to remain correct if collection stops in the middle of
   the copied prefix.

Do not globally deduplicate equal turn IDs across unrelated sessions. Fork
suppression must be constrained by a verified parent-child relationship.

At scale, do not rescan the entire parent rollout on every collection cycle.
Resolve the inherited boundary when the fork is first discovered and persist a
bounded marker or inherited-turn state in the cursor.

### Claude internal and sidechain usage can be omitted

Claude session summaries include assistant usage from metadata work and
sidechains, while human-turn construction intentionally ignores those records.
If dashboards read only human `turn.usage` facts, those tokens disappear from
the product total.

When the product promise is "all tokens," every real model invocation must be
represented exactly once. Internal or sidechain usage may be attached to its
own stable `Other`/internal turn or to a well-defined parent turn, but it must
not be counted in both places and must not be discarded merely because it has
no human prompt.

### Completion-only reporting creates delayed spikes

Turn usage is emitted only when a turn completes or aborts. A long tool loop may
accumulate millions of cache-read tokens before it becomes visible, and all of
that usage is assigned to the completion time. This is reporting latency and
time-bucket behavior, not necessarily overcounting.

Product and incident analysis must distinguish:

- final accounting correctness;
- near-real-time visibility;
- attribution to the day on which a turn completed.

If partial-turn reporting is introduced, it must use stable incremental IDs or
cumulative replacement semantics. Re-emitting a growing turn as independent
deltas will inflate totals.

### Cursor seeding can intentionally lose history

The first Agent observation does not backfill historical sessions. Rollout
replacement, truncation, an undiscovered pre-cutover session, or a turn whose
start lies before the seed tail may also cause Agent to seed a new baseline
instead of replaying data. This protects against duplication but can undercount.

Any change to this trade-off must explicitly test:

- a newly created session;
- an old session resumed after Agent initialization;
- rollout truncation or path replacement;
- a partial final JSONL line;
- a turn spanning multiple collection cycles;
- an interrupted turn without a normal completion record.

### Late or revised Claude turns can fall behind the cursor

The Claude cursor advances by `ended_at` and remembers hashes only at the most
recent timestamp. A newly discovered turn with an older timestamp, or a
previously emitted turn whose usage is later revised, cannot advance that
cursor and may be omitted.

Transcript merging and cursor tests must cover overlapping resume files,
late-arriving records, identical timestamps, and revised message/turn records.
Message IDs must be deduplicated across files for the same logical session, not
only within each individual file.

### Counter resets and tier corrections need explicit semantics

Cumulative counters are normally monotonic. Component-wise negative clamping
can hide a reset and can overcount if a provider reclassifies tokens between
fresh input and cache tiers.

On any component decrease, Agent must either:

- recognize a documented counter reset and seed a new baseline; or
- emit an observable warning and suppress an unsafe delta.

It must not silently manufacture a positive all-tier delta from incompatible
before/after counters.

The Codex collector records a reset marker as soon as any intermediate
`token_count` observation decreases. The completed turn is suppressed, the
latest cumulative value becomes the next baseline, and Agent emits an
`adapter.warning`. Comparing only the turn's first and final observations is
insufficient because a reset can recover past the old baseline before the turn
ends.

## Delivery and idempotency

Every usage event must have a stable ID derived from stable logical identity,
not collection time. Local queue retries, lost HTTP responses, Control outbox
retries, and process crashes must all replay the same event ID.

Control must accept both the previous and next `total_tokens` encoding during a
rolling upgrade, canonicalize the stored total itself, and validate the tier
fields. Agent must start in the legacy effective-total wire mode until a
heartbeat explicitly advertises `turn_usage_total: all_tier`. This handshake
makes both rollout orders safe:

1. Old Agent to new Control: Control accepts the effective total and stores the
   canonical all-tier total from the four tier fields.
2. New Agent to old Control: old Control does not advertise the capability, so
   Agent keeps sending the effective total.
3. New Agent to new Control: the heartbeat enables all-tier wire totals.

Control-first remains the preferred operational order because it enables the
new contract immediately and simplifies observation, but correctness must not
depend on that order.

An incompatible HTTP 400 response is permanent from Agent's perspective: the
event is quarantined and is not automatically retried after Control upgrades.
Every protocol rollout therefore needs an explicit quarantine recovery plan.

Agent's queue has bounded retention and capacity. Events older than 30 days or
events evicted after the queue exceeds its byte limit are intentionally lost.
Operational monitoring must alert on expired, capacity-evicted, and quarantined
usage events.

## Migration rules

Historical migrations must be idempotent and reconstruct values from an
authoritative source before mutating product totals.

Never clear every session total and restore only sessions that happen to have
new-format turn rows. Older sessions may have only legacy usage facts, and a
global clear silently changes them to zero.

Before an all-tier migration:

1. Project all pending usage events.
2. Remove or suppress known fork-inherited duplicate facts.
3. Determine which sessions have reconstructible four-tier history.
4. Preserve and explicitly mark legacy totals that cannot be reconstructed.
5. Recompute ClickHouse and PostgreSQL totals from the same deduplicated facts.
6. Verify organization, user, model, day, and session totals reconcile.
7. Make concurrent ingestion safe, or run the migration while ingestion is
   stopped by an explicit operational procedure.

A migration must not bake known duplicate fork rows into a new canonical total.
Fixing future collection is insufficient; existing analytical rows require a
separate correction plan.

Control reconstructs migration totals from the durable PostgreSQL outbox while
holding the ingestion boundary locks. Verified child events whose turn IDs also
exist in their declared parent are written to the non-destructive ClickHouse
`turn_usage_exclusions` ledger. Dashboard, efficiency, work-type, Prometheus,
remote-write, and session-repair queries all exclude those event IDs; the raw
turn facts remain available for audit.

## Required regression tests

Any token-accounting pull request must include the relevant cases below.

### Provider normalization

- Codex input containing a cached subset.
- Claude disjoint input, cache read, and cache creation.
- Cache-dominant usage where the all-tier total is much larger than fresh input.
- Reasoning output already included in output.
- Missing, zero, negative, reset, and malformed counters.

### Forks and sessions

- Parent turn emitted once before a fork.
- Child rollout containing the complete copied parent prefix.
- Child emits only its unique delta.
- Collection split in the middle of the inherited prefix.
- Missing parent rollout uses a conservative, non-duplicating fallback.
- A chain of multiple forks does not multiply inherited history.
- Retry after cursor commit emits nothing.

### Claude transcripts

- Human turn with a tool loop.
- Sidechain and metadata model usage represented exactly once.
- Interrupted and unfinished turns.
- Same session across multiple files with overlapping message IDs.
- Late records and equal completion timestamps.
- A transcript line exceeding the scanner limit produces an observable warning
  rather than silently dropping the whole session.

### Delivery and migrations

- Old Agent to new Control.
- New Agent defaults to the effective-total wire encoding with an old Control.
- New Control advertises all-tier totals and accepts both wire encodings.
- Retry before and after queue acknowledgement.
- Crash between queue append and cursor commits.
- Sessions with only legacy usage survive migration.
- Existing fork duplicates are not included in migrated totals.
- Large-volume tests cover query latency, migration duration, cursor-state
  growth, and queue pressure.

## Release checklist

Before shipping a token-accounting change, reviewers must answer all of the
following:

- Are the stored tiers disjoint for every provider?
- Does `total_tokens` include all four tiers exactly once?
- Are forks, resumes, retries, and overlapping files idempotent?
- Is every real model invocation represented, including internal work?
- What happens to active and historical sessions during upgrade?
- Can old Agent and new Control coexist during rollout?
- Can new events become permanently quarantined?
- Does the migration preserve sessions without new-format history?
- Are existing bad rows repaired, not only future rows prevented?
- Are totals consistent across Dashboard, Session, Efficiency, Prometheus, and
  remote write?
- Does the design remain bounded when rollout files, turn counts, and stored
  usage reach production scale?

If any answer is unknown, the change needs a design update before implementation
or release.
