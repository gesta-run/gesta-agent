# AGENTS.md - Engineering Conventions for AI Coding Agents

These rules are mandatory for every change in this repository.

## Commits

- Sign off every commit with `git commit -s`.
- Commit as a CloudPilot work email, never a personal email.
- Every pull request must be exactly one commit. Squash locally before pushing:
  `git reset --soft <base-branch> && git commit -s -m "<subject>"`.

## Pull Requests

- Open pull requests as ready for review. Never open draft pull requests.
- Never push new work onto an already-merged pull request branch; branch off fresh `origin/main` and open a new pull request.

## Language

- All code and Git artifacts must be English-only.

## Before Opening a Pull Request

- Run the full build and tests locally and confirm they pass.
