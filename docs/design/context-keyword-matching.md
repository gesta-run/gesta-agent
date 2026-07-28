# Context Keyword Matching

Status: approved for implementation on 2026-07-28.

## Problem

`keyword_any` previously used case-insensitive substring matching. Short
keywords could therefore match unrelated words: the keyword `PR` matched
`promote` and caused organization context to appear on the following turn.
The matcher also lowercased the full prompt once per keyword, multiplying an
allocation and a full prompt scan by the number of configured keywords.

## Product contract

`keyword_any` remains a literal, case-insensitive matcher, with these boundary
rules:

- If a keyword edge is an ASCII letter, digit, or underscore, that edge must
  be at an ASCII word boundary.
- Non-ASCII text does not create an ASCII word character. This preserves
  continuous CJK matching and allows mixed text such as `提PR` and `PR已创建`.
- Punctuation at a keyword edge does not require a boundary on that edge.
- Partial-word, morphological, or fuzzy matching is not implicit. Rules that
  need it must list the intended forms or use `regex`.

Examples:

| Keyword | Prompt | Result |
| --- | --- | --- |
| `PR` | `open PR #42` | match |
| `PR` | `提PR` | match |
| `PR` | `promote` | no match |
| `deploy` | `deploy the service` | match |
| `deploy` | `deployment status` | no match |
| `发布` | `请发布服务` | match |

The JSON protocol and `keyword_any` identifier remain unchanged. The semantic
contract is shared by Control, Console, and Agent releases; mixed deployments
temporarily retain the behavior of each installed Agent version.

## Existing rules

Existing rules must be reviewed as part of rollout:

- Remove keywords that differ only by letter case.
- Keep short acronyms such as `PR`; the new boundary semantics remove their
  unrelated-word false positives.
- Add intentional word forms explicitly when a rule should match them.
- Use `regex` only when the rule deliberately requires partial-word matching.

No historical match events are reclassified.

## Implementation

The Agent trims the prompt once and lazily computes one lowercase prompt only
when an active `keyword_any` rule is evaluated. Every literal keyword reuses
that value and checks the two relevant bytes around each candidate match.

Control normalizes keyword arrays case-insensitively on writes and reads. Read
normalization makes legacy case-duplicate rows conform without a database
migration. Console explains the boundary contract next to the keyword editor.

The keyword path remains `O(r * k * n)` for `r` rules, `k` keywords per rule,
and prompt length `n`, but removes the former `O(r * k * n)` prompt-lowercasing
work and its repeated allocations.

## Rollout and validation

1. Deploy Control and Console contract changes.
2. Update existing keyword rules with their explicit intended forms.
3. Release the Agent matcher.
4. Verify `PR` against Chinese adjacency, delimiters, `prompt`, `promote`, and
   alphanumeric suffixes.
5. Verify a matched turn creates one notice on the immediately following turn
   and does not repeat on the third turn.
