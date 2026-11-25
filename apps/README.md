# `apps/` - first-party Helm charts

The charts written here are published as OCI by CI (`deps-publish-apps`) and pinned by the
`Component`s in [`platform/components/`](../platform/components/). Third-party charts
(metallb, cert-manager, traefik) are mirrored from upstream and declared in
[`manifests/dependencies.yaml`](../manifests/dependencies.yaml).

| Chart | Role |
|---|---|
| `downstream-bootstrap` | MetalLB pool on the cluster's ingress IP, cert-manager issuers |
| `local-path-provisioner` | the default `local-path` storage class of rke2 clusters |

## Changing a chart

1. Edit `apps/<chart>/` and bump `version` in `Chart.yaml` (`check-chart-versions.sh` requires it).
2. Bump `spec.chart.version` in `platform/components/<chart>.yaml`.
3. If the rendering gains or loses a resource, update the list of kinds passed to
   `check-chart-schemas.sh` in CI: it is the only check of these third-party CRs, whose
   schemas kubeconform does not have.
4. After the merge, `rondo deploy` applies the `Component` at the new version.
