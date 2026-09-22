# Project tracking

[GitHub](https://github.com/alcxyz/paw/issues) is the canonical tracker from
2026-09-22. The immediate priority is the focused pilot, followed by broader M1
qualification and only then a second runtime backend.

Open work was migrated as reviewed summaries. Private discussions and local
review reports were not published. Older review documents and commit messages
retain historical issue numbers; use this map for current planning.

| Previous issue | Current GitHub issue | Work |
| --- | --- | --- |
| 38 | [#1](https://github.com/alcxyz/paw/issues/1) | Focused cross-device pilot |
| 40 | [#2](https://github.com/alcxyz/paw/issues/2) | Build and nightly-update qualification |
| 34 | [#3](https://github.com/alcxyz/paw/issues/3) | Second runtime backend |
| 30 | [#4](https://github.com/alcxyz/paw/issues/4) | Bounded external egress |
| 29 | [#5](https://github.com/alcxyz/paw/issues/5) | Kubernetes networking portability |
| 27 | [#6](https://github.com/alcxyz/paw/issues/6) | Layer-count budget contract |
| 26 | [#7](https://github.com/alcxyz/paw/issues/7) | Native prebuild pruning |
| 23 | [#8](https://github.com/alcxyz/paw/issues/8) | Backing-volume reclamation |
| 8 | [#9](https://github.com/alcxyz/paw/issues/9) | Broad workflow qualification |
| 7 | [#10](https://github.com/alcxyz/paw/issues/10) | Workspace security boundary |
| 6 | [#11](https://github.com/alcxyz/paw/issues/11) | Platform-readonly qualification |
| 4 | [#12](https://github.com/alcxyz/paw/issues/12) | Image release qualification |

The previous private repository remains a historical source copy. It is not a
second authoritative tracker or a required CI gate.

## State-preserving upgrades

[Issue #14](https://github.com/alcxyz/paw/issues/14) tracks persistent repository
storage, read-only preflight, consistent backup/recovery, controlled single-writer
upgrades, and explicit legacy migration in the focused-pilot milestone. See
[the upgrade guide](upgrades.md) for the implemented stage and remaining limits.
