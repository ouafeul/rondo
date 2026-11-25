---
name: Bug report
about: Something RONDO does wrong
labels: bug
---

## Symptom

<!-- What you saw: command and output. Not the diagnosis. -->

## Steps to reproduce

1.
2.

**Expected:**
**Actual:**

## Environment

| | |
|---|---|
| Cluster | <!-- management, downstream `<name>`, sandbox, CI --> |
| `rondo version` | |
| Channel | <!-- stable `vX.Y.Z` or `edge-main-<sha>` --> |

## State

<!-- Raw output, before any reasoning. -->

```console
$ kubectl -n rondo-system get targets,components,enrollments
$ kubectl -n rondo-system logs job/<name>
```
