# CLAUDE.md

## Pull Requests

- Title format: `type(scope)!: [level] subject` ([conventional commits](https://www.conventionalcommits.org/en/v1.0.0/)); the subject starts with exactly one of `[high]`, `[medium]`, `[low]`. See [README.md#contributing](README.md#contributing).
  - `[high]`: Deep review for header verification, sync/head selection, store, p2p exchange, security, or other changes with significant risk.
  - `[medium]`: Standard review for code changes with limited impact and well-understood behavior.
  - `[low]`: Focused review for straightforward docs, tests, or tooling changes with low risk.
- Use the higher level when unsure; a small diff can still need deep review.
- Include the title's review tag in the description with a brief reason, e.g., `Review level: [low] — documentation only.`

## Security PRs

- For PRs that resolve Hacken bug bounty reports, do not include details about the bug in the PR description. Instead, link to a Linear issue that contains more details on the bug and the link to the Hacken bug bounty report.
