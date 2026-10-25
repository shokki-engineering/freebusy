<!-- Copyright 2026 Srikanth K. -->
<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

## What changed

<!-- One or two sentences. -->

## Checks

- [ ] Every commit is signed off (`git commit -s`), agreeing to the [CLA](CLA.md)
- [ ] `just check` passes, and `just test-live` if the change touches the database
- [ ] Generated code was regenerated with the plugins, not edited by hand
- [ ] api-linter is clean for any `.proto` change
- [ ] No Go import aliases
