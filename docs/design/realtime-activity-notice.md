# Real-time Activity Notice

## Decision

Keep one footer line on every accepted user turn while tracking three
independent values:

- targeted organization context applied to the current prompt;
- unique memories recalled during the current turn, including automatic recall
  and later local memory searches;
- equivalent LOC from the latest completed turn.

The final line is resolved through the local agent immediately before the model
answers:

```text
Gesta · Context 1 · Memory 5 · Last output 29 eLOC · Details
```

The footer must still be returned when every value is zero. Suppressing that
case leaves the previous turn's footer as the newest visible link, which makes
current-turn `Context` and `Memory` look delayed even though their activity
record is already current.

## Flow

1. `UserPromptSubmit` creates a local activity record independently of local UI
   health, so a transient health probe cannot delay or overwrite state.
2. Context matching records targeted matches in that activity.
3. Automatic memory recall records returned fact IDs in that activity.
4. The injected memory search command carries the activity ID, so successful
   `/api/v1/memory/search` calls add unique recalled facts to the same activity.
5. The previous turn's eligible output summary is attached to the activity.
6. Immediately before the final response, the model calls
   `POST /api/v1/activity/notice` with the activity ID and emits the returned
   non-empty `notice` field verbatim. The details link always uses that current
   activity ID. Connection failures are silent.
7. `Stop` measures the current turn and saves it for the next turn.

## Timing contract

| Footer field | Source turn | Reason |
| --- | --- | --- |
| `Context` | Current | Targeted context matching finishes before the answer. |
| `Memory` | Current | Automatic recall and explicit local searches update the current activity. |
| `Last output` | Previous completed turn | Current output is incomplete until `Stop`. |
| `Details` | Current | The footer must open the activity created for the current prompt. |

`Context` counts targeted matches only; organization context configured for
every prompt is intentionally excluded. `Memory` counts unique recalled facts,
not memory writes or proof that the model used a fact.

## Equivalent LOC

The local notice follows the control-plane formula:

```text
eLOC = code lines + config lines + test lines
     + (documentation words + other prose words) / 8
```

Only measurements with `efficiency_eligible=true` enter the notice. Raw Gross
Ink telemetry remains unchanged.

## Boundaries

- `Memory` means recalled, not proven semantic use by the model.
- Current-turn output cannot be complete before `Stop`; the notice therefore
  labels it `Last output`.
- Activity records stay local, expire after 24 hours, and store no raw tool
  arguments.
- The internal notice lookup is filtered at the agent observation boundary; it
  is not an MCP-domain rule and does not affect ordinary tool input metering.
- The endpoint is loopback-only and uses the unguessable activity ID as a
  capability.

## Compatibility and rollout

- Keep `POST /api/v1/activity/notice`, its request header, JSON schema, footer
  labels, and details URL unchanged.
- The only behavior change is that a valid current activity returns a footer
  even when context, memory, and previous output are all zero.
- Older agents may still suppress an all-zero footer during a mixed-version
  rollout. No client or control-plane migration is required.
- Activity storage remains bounded at 256 records with a 24-hour TTL. Always
  formatting the existing record adds no network request, persistent write, or
  unbounded data growth.

## Implementation and verification

1. Remove the all-zero early return from `formatActivityNotice`.
2. Replace the zero-value suppression test with an assertion for a current-turn
   zero-value footer and details URL.
3. Keep the existing test that combines current context and memory with the
   previous completed output.
4. Run the targeted `pkg/localactivity` tests. Full repository tests remain a
   pre-PR check.

## Architect and product review

- High-confidence issue: suppressing all-zero results makes the interface retain
  a stale-looking footer and violates the current-turn details-link contract.
- Compatibility risk is limited to increased footer frequency; API consumers
  see the same response shape and labels.
- The smaller version is sufficient: delete one suppression branch and update
  its test. A UI refresh channel, delayed finalizer, queue, new endpoint, or
  storage schema would be over-engineered.
- The footer adds some visual noise on inactive turns, but showing explicit
  zeroes is preferable because it proves that current-turn evaluation ran and
  gives the user the current details link.
