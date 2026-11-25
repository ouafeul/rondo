# `downstream-bootstrap` - bootstrap glue for downstream clusters

First-party chart that carries the **glue CRs** missing from freshly provisioned downstream RKE2
clusters:

- **MetalLB pool** (`IPAddressPool` /32 + `L2Advertisement`) on the cluster's own ingress IP, the
  same single-IP model as the management cluster. The IP comes from the Target's values
  (`ingressIP: ${target.values.ingressIP}` in
  [`platform/components/downstream-bootstrap.yaml`](../../platform/components/downstream-bootstrap.yaml)).
- **cert-manager PKI** (selfSigned `ClusterIssuer` -> CA `Certificate` `cluster-ca` -> `ClusterIssuer`
  `cluster-ca-issuer`), mirroring the management cluster, **without** the `rancher-tls` cert
  (management-only).
- **Artifactory CA** as a Secret in the namespaces listed in `artifactoryCA.namespaces`, when
  `artifactoryCA.pem` is set.

## Context

Unlike the management cluster (k3s, building blocks installed by helmfile), downstream RKE2 clusters
start bare. The **engines** (MetalLB, cert-manager, Traefik) are delivered by their own `Component`s
(mirrored OCI charts); this chart only carries the **CRs** that configure them (pool, issuers). It
therefore expects the `metallb.io` and `cert-manager.io` CRDs to be established: its `Component`
declares `dependsOn` on `metallb` and `cert-manager`, and its `Enrollment` sets
`skipDryRunOnMissingCRD: true` to tolerate a CRD that is not established yet.

> No `dependencies:` subcharts here (CI's `helm dependency update` does not substitute
> `${REGISTRY_HOST}`). The engines are separate Components, not subcharts.
