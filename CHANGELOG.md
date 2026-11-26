# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.0-rc.1] - 2026-11-26

First release candidate. RONDO deploys components onto air-gapped infrastructure with three objects and three actions:
declare a site, install a component, enroll it on a target.

### Added

#### Data model
- **Three CRDs.** `Component` (what to deploy), `Target` (where: `kubernetes` or `vsphere`,
  with `roles`) and `Enrollment` (one component on one target, selected by name or role).
  Deleting an Enrollment uninstalls its workload.
- **Site manifest `rondo.yaml`**: `artifacts` (the site mirror), `targets` (`targets.vsphere[]`,
  `targets.management`, `targets.downstreams[]`) and `secrets` (including
  `secrets.k3sClusterToken` and `secrets.vcenterPasswords`). Hostnames derive from
  `ingressBaseDomain`.

#### CLI (`rondo`)
- `rondo deploy` deploys the management cluster, its vSphere Targets and the RONDO components
  from the operator workstation.
- `rondo deploy downstreams` provisions rke2 clusters (one tofu state per target), declares
  them as Targets and installs the base layer on them.
- `rondo component install` applies a component tree directly and prunes the objects of the
  previous install that the tree does not contain. `rondo component uninstall` deletes the Enrollments first, waits for them to be gone
  (30 minutes at most) so their finalizers can run the uninstall Jobs (`helm-uninstall`,
  `vm-destroy`), then deletes the Components; prune follows the same order. If the wait
  fails, the Components and the tree record are kept and re-running the command resumes.
- `rondo component lint` checks `components/` and `enrollments/` against the CRD schemas.
- `rondo init` writes a site manifest template.
- A tofu plan that destroys anything asks for confirmation.

#### Controller
- Machine-based components (archetypes B and C, `spec.vm`): VMs provisioned by tofu and
  configured by the RONDO playbook over SSH. The `bundle_install` role re-extracts an archive
  whenever its checksum changes, even under the same file name.

#### Component sandbox (`rondo-sandbox`)
- **`rondo-sandbox`** runs the `rondo` CLI commands against k3d clusters on the workstation,
  with the CLI's own code (`internal/sandbox` replays `internal/steps`):
  - `deploy` deploys the management cluster, RONDO, its vSphere Targets and the site Secrets;
    `deploy downstreams [target…]` creates one k3d cluster per downstream, declared as an rke2
    Target with its base layer.
  - `component install [directory]` publishes what the component's CI would publish, applies
    its tree like `rondo component install` and waits for RONDO's verdict: every Enrollment
    converged and `Ready`, or the first failing one with its cause and its Job's log.
  - `component uninstall`, `destroy downstreams` and `destroy` tear down.
  - Sandbox-only commands: `status`, `check` (static checks, no sandbox needed), `why` (what
    is blocking), `values` and `template` (what RONDO applies), `pods` and `tail` (pods and logs
    of an Enrollment, on its cluster), `restart` (reloads an image rebuilt under the same tag),
    `dashboard` (the RONDO dashboard over port-forward), `tui` (`rondo tui` on the sandbox, read
    only), `ssh <machine>` and `kubeconfig` (the kubeconfig of the cluster of a Target, or of
    the one holding a role).
- The site is `rondo-sandbox.yaml`, with the `rondo.yaml` keys (`targets.downstreams[]`,
  `targets.vsphere[]` `{name, roles, componentValues}`, `secrets.components`) minus what the
  workstation provides, plus `targets.management.roles` for a single-cluster sandbox, and what
  the sandbox cannot guess: CI images, `ports` from the workstation to ingresses
  (`role` or `machine` entries, `protocol: tcp` or `http`, shown by `status` as a
  `http://localhost:<port>` link), `artifacts` and `rpmRepos`. `init` writes it from the
  component tree.
- `rondo-sandbox component new <directory>` writes a hello-world component under that name that
  installs as is: a first-party chart with its image (`apps/`, `build/`), a mirrored upstream
  chart (`manifests/dependencies.yaml`), a machine (`vm/payload/`, install and verify phases),
  role-based Enrollments and `rondo-sandbox.yaml`. `--archetype a` or `b` keeps only one kind.
- Several components share one sandbox; only what a real site would refuse is refused (an
  Enrollment without a Target, a release already delivered on its Target, a Component owned by
  another component).
- A stand-in mirror (zot + nginx, `artifacts.preset: generic`) serves charts, images, files, an
  OpenTofu OCI provider mirror and the http state backend, pulled from the Internet on demand. RONDO itself comes from it: the binary embeds the tree of its version (CRDs, base
  layer, RONDO charts) and pulls the `rondo-controller` and `rondo-toolbox` images;
  `--rondo-version` uses the images of another version, `--rondo-from <clone>` builds RONDO from
  a local clone.
- Machines run as Rocky Linux 9.9 containers (`rondo-sandbox-machine` image) named
  and addressed like the VMs of `terraform/vm`, with systemd, sshd, the `rondo` account and the
  mirror CA. The controller and its Jobs run as on a real site: `vm-apply` runs a machine module
  with the same variables, outputs and inventory as `terraform/vm`, its tofu state on the
  sandbox mirror; `vm-install` runs the RONDO playbook over SSH. A machine keeps its IP
  across installs; `component install --fresh` recreates the component's machines from
  scratch.
- `deploy`, `deploy downstreams` and `component install` refuse to run when Docker's disk is
  85 % full (the threshold at which the k3d nodes' kubelet evicts pods) and stop on a node in
  `DiskPressure`.
- Bash, zsh and fish completion (`rondo-sandbox completion <shell>`): commands, flags,
  downstreams, components, installed Enrollments and cluster Targets.
- Runs on Linux and macOS workstations, amd64 and arm64.
- Documentation: [components/sandbox](docs/src/content/docs/components/sandbox.mdx).

#### Terminal UI (`rondo tui`)
- A terminal interface combining the dashboard and the CLI: the dashboard views (read from the
  dashboard itself over `kubectl port-forward`; its API lives in `internal/dashapi`, so the
  `rondo` binary does not depend on client-go), the installed components, the environments and
  their `rondo.yaml`, and the `deploy`, `destroy`, `component`, `env` and `init` commands run
  as subprocesses with their steps, logs and confirmations.
- Components and Enrollments grouped by installed component: one root per installation
  (`acme`, `rondo` for the RONDO layer…), with its Components or Enrollments underneath, folded
  and unfolded with space, all at once with `-` and `+`. Enter on a root opens what the
  installation deployed: its charts and machines, then their Enrollments, Target and health.
- Navigation: the view menu takes focus with `tab` (or ←) and switches views with ↑↓, → or
  Enter; the focused pane has a coloured border; a breadcrumb shows the full path.
- Mouse support: click to focus a pane, switch views, select a row (double-click opens it),
  fold or unfold, trigger the bottom bar's key actions, jump back to a breadcrumb level or
  open an underlined name; the wheel scrolls the pane under the pointer. `M` hands the mouse
  back to the terminal for text selection.
- `rondo.yaml` editor (`e`, `E`): the manifest is validated on every keystroke as `rondo`
  validates it, and the error points at its line and YAML path
  (`targets.vsphere[0].vmNetwork.gatewayIp: must be an IP address`). Saving is atomic and
  keeps the file mode. `config.Parse` and `config.Explain` validate and locate errors in a
  manifest that is not on disk yet.
- A read-only mode (`ReadOnly`) that refuses commands, forms and the `rondo.yaml` editor,
  says so in the header and greys out what it refuses.

#### Artifact mirrors
- Vendor-neutral site mirror: `artifacts:` with a `generic` preset (any OCI registry and any HTTP
  file server) or an `artifactory` preset that derives every endpoint from one URL. With
  `generic`, Helm charts default to `<oci.repository>/charts`.
- Anonymous and publicly trusted mirrors: `artifacts.username`, `secrets.mirrorPassword` and
  `artifacts.caCertificateFile` are optional; without them RONDO skips registry logins,
  `regcred` and basic auth, and uses the system trust store.
- OpenTofu providers from an OCI registry (`artifacts.tofu.providerOCI`, `oci_mirror`) or a
  network mirror (`artifacts.tofu.providerMirror`); state on an http backend
  (`artifacts.tofu.state.http`) or S3-compatible storage (`artifacts.tofu.state.s3`, MinIO, Ceph,
  AWS; lock file next to the state).
- Nodes pull public and rke2 system images through registry mirrors that rewrite them under
  `<oci.registry>/<oci.repository>`.
- Component contract: site tokens `${OCI_REGISTRY}`, `${IMAGES_REPOSITORY}`,
  `${CHARTS_REPOSITORY}`, `${CHARTS_OCI}` (a chart is `${CHARTS_OCI}/<chart>`), `${FILES_URL}`,
  `${FILES_REPOSITORY}`, `${RPM_REPOSITORY}`; Target value `mirrorCA`; `rondo-platform-values`
  keys `mirrorUser`, `mirrorPassword`, `mirrorAuthURL`; Job environment `MIRROR_*`; Ansible
  variables `rondo_mirror_*`. The Secret `mirror-ca` (key `ca.crt`) always exists in the
  namespaces the downstream base layer lists, empty for a publicly trusted mirror.
- Mirror scripts (`scripts/ci`, `.github/workflows/mirror.yml`) fill any OCI registry
  (`--oci-registry`, charts under `<registry>/charts` or `--charts-oci`), any HTTP file server or
  S3 bucket (`--files-url`) and RPM server (`--rpm-url`, `repodata/` built with
  `createrepo_c`); uploads are idempotent through `.sha256` sidecars; OpenTofu providers go to a
  static network mirror; downloads are scanned with ClamAV and Trivy.
- `scripts/mirror-release.sh`, shipped with each release, copies one release (images, charts,
  files, packages, channel pointer) into a site mirror; `--dry-run` prints the copy list.
- Documentation: [Artifact mirrors](https://ouafeul.github.io/rondo/operations/artifact-mirrors/).

#### Packaging and distribution
- GitHub Releases carry the `rondo` and `rondo-sandbox` binaries, `install.sh`,
  `rondo.yaml.example`, the IaC bundle, the VM module and Ansible content, `rondo` and
  `rondo-sandbox` rpm packages and the `rondo-sandbox` deb package (shell completion included),
  `mirror-release.sh` and `SHA256SUMS`.
- Images `ghcr.io/ouafeul/rondo-controller`, `rondo-toolbox`, `rondo-component-toolbox`,
  `rondo-sandbox-machine`, with SBOM and provenance; first-party charts at
  `oci://ghcr.io/ouafeul/charts`.
- An end-to-end workflow runs the sandbox scenario (charts, images, a machine) through a generic
  mirror every week and on demand.

### Security
- Platform on supported releases: Rancher 2.15.3 on k3s 1.36.6, rke2 1.36.5 clusters,
  cert-manager 1.21.3, MetalLB 0.16.2 (layer 2, no FRR images), Traefik 3.7.20 (chart 42.4.3),
  local-path-provisioner 0.0.37; tools OpenTofu 1.13.10, kubectl 1.36.6, Helm 3.22.1, helmfile
  1.8.5, helm-diff 3.15.15, k9s 0.51.0, yq 4.54.3, ansible-core 2.22.0; providers rancher2
  15.3.0, vsphere 2.18.0, cloudinit 2.4.1; images on Go 1.27.4, Debian 13, distroless Debian 13,
  Python 3.14 and Rocky Linux 9.9, OS packages upgraded at build.
- Workstation tools are verified against the sha256 pinned in `manifests/dependencies.yaml` and
  the `<file>.sha256` served by the mirror; a mismatch is refused.
- The local-path-provisioner chart runs non-root with a read-only root filesystem, no privilege
  escalation, all capabilities dropped and the RuntimeDefault seccomp profile.
