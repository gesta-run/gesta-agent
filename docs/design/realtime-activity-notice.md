# Real-time Activity Notice

## Decision

Keep one footer line while tracking three independent values:

- targeted organization context applied to the current prompt;
- unique memories recalled during the current turn, including automatic recall
  and later local memory searches;
- equivalent LOC from the latest completed turn.

The final line is resolved through the local agent immediately before the model
answers:

```text
Gesta · Context 1 · Memory 5 · Last output 29 eLOC · Details
```

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
   `notice` field verbatim. Connection failures are silent.
7. `Stop` measures the current turn and saves it for the next turn.

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

## Minimality review

One mutable local activity record is sufficient. Separate queues, remote API
changes, and three detail pages add no value. The only extra model action is one
loopback status request immediately before the final response.
