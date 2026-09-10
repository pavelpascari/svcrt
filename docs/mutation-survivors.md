# Mutation Test Survivors

This document records mutants that survive the mutation testing threshold for each module. Modules with perfect coverage (all mutants killed) have no entry.

## `config` Module

**Mutation Score: 1.0 -- no survivors.**

Every mutant go-mutesting generates for this module is killed by the test
suite. This entry is updated only when that stops being true; the exact
mutant total is expected to grow as the module grows, and is not tracked
here (see `./scripts/mutation.sh config` output for the current total).
