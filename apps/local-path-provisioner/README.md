# local-path-provisioner

local-path volume provisioner for **downstream RKE2** clusters.

## Why this chart exists

RKE2 ships **no** volume provisioner, unlike k3s, which provides one on the management cluster.
Without it, every PVC stays `Pending` forever and the pods waiting on it are never scheduled:

```
0/6 nodes are available: pod has unbound immediate PersistentVolumeClaims
```

Any workload that claims volumes on an RKE2 cluster (for example an observability stack:
Prometheus, Alertmanager, Grafana, VictoriaLogs) must therefore declare `local-path-provisioner`
in its `dependsOn`. Without that ordering it would start before the StorageClass exists and fall
back to `Pending` through a plain race.

## Why a first-party chart

Upstream does not publish this chart to a Helm repository: it only lives in the source tree of
`rancher/local-path-provisioner`. There is nothing to mirror in `helm_charts`, so the chart is
written here and published as OCI by CI (`deps-publish-apps`), like `downstream-bootstrap`.

## The two images, and the helper trap

`values.yaml` holds the **upstream** images (`docker.io/rancher/...`, `docker.io/library/...`), and
that is a contract with `scripts/ci/extract-helm-images.py`: it renders this chart offline,
neutralizes registry overrides and falls back on these values to list what must be mirrored.
Pointing them at the artifact mirror would blind the mirror to its own source. The `Component`
overrides them to the mirror at deploy time.

The **helper pod** image needs special care. It normally lives inside a ConfigMap *string*
(`helperPod.yaml`), invisible to a walk of YAML structures: it would be missing from the mirror
with nothing to flag it. That is why it is repeated as `--helper-image` on the Deployment's command
line, where the extractor finds it (it reads `args`). Do not remove this repetition to "simplify".

## Not for the management cluster

The `Enrollment` targets `each-rke2-target`, so only downstream RKE2 clusters get it. Enrolling the
management cluster would add a **second** default StorageClass next to the k3s one.
