# `ansible/` - first-party Ansible content

The Ansible content **written here** is packaged as a tarball by CI (the **`rondo-vm-runtime`** job,
**on tag**) and pushed under `generic/ansible/rondo/<tag>/rondo-ansible.tar.gz`. It is played by the
`rondo-exec vm-install` Job against the inventory produced by `terraform/vm/`.

The tar root holds `ansible.cfg` + `site.yml` + `roles/`, so that `ansible-playbook site.yml` works
from the extracted directory.

> Versioned **by the tag**, like the rest of the machinery: the operator builds the URL from its own
> version. A `vX.Y.Z` controller therefore plays the `vX.Y.Z` roles, not those of another release.
> The machine module (`terraform/vm/`) is published by the same job, for the same reason.

This is not per-component content: it is the **generic runtime** of archetypes B and C.
A component author writes **neither HCL nor Ansible**; they describe their component in a
`component.yaml` (`spec.vm`), and this role does the work.

## What the `bundle_install` role does

In this order (as in `tasks/main.yml`), each step idempotent:

1. **waits for the pre-stage to finish** (`/var/lib/rondo-cloud-init-done`). Without this wait, dnf would
   fail on a lock still held by the running cloud-init. A missing marker **warns** without blocking;
2. **creates the component's working directory** (`/opt/rondo/<component>`), one per component: two
   components on the same machine must not step on each other, and a recovery `rm -rf` must be safe;
3. **configures the RPM repositories** (`spec.vm.rpmRepos`) as `.repo` files pointing at Artifactory,
   with the CA and the credentials;
4. **downloads the artifacts** (`spec.vm.artifacts`) from Artifactory, with checksum verification, and
   extracts them when asked;
5. **renders the configuration file** (`spec.vm.config` + the group's) in shell format, telling apart
   scalars, booleans and **arrays**;
6. **writes the secrets file as `0600`**, **runs the installer's phase sequence**, then **destroys** the
   file (`shred`), whatever the outcome.

## Where packages come from: the component, not the rondo mirror

**A component's packages are published by the component's OWN CI.** `rpm_packages: []` in the manifest
is a decision, written as a comment at that spot: `demo-mft` (the reference component of archetype C,
shipped from its own repository) builds its own YUM repository (`createrepo_c`), scans it, and pushes
it to `rondo-rpms-multi-local/mft/<version>`; its installer consumes it through its baseurl and writes
`module_hotfixes=1` itself.

The criterion is **capability**, not ownership: the rondo mirror accepts no checksum or signature
field, so it cannot verify a vendor's PGP signature, whereas the component's CI does, and **republishes
the signature** next to the archive. Keeping the same list on both sides would mean two repositories,
two mechanisms, the same packages, and guaranteed drift at the first version bump of the COTS.

`rpm_packages` stays **declared** in the manifest for a component that does **not** publish its own
packages: adding an entry there brings the package in through the rondo mirror. The base platform pins
its packages elsewhere (`base_images`).

What this means for the role: nothing more than a baseurl. `spec.vm.rpmRepos` describes an Artifactory
repository, whether it is fed by the mirror or by the component's CI. The role never builds a `file://`
repository extracted from a bundle: that would be a second way into the air gap.

## The per-phase time bound, and what it guarantees

**An installer is a family of entry points, not a script**: `spec.vm.installer.phases` is an ordered
sequence, each phase with its own arguments and its own time bound (in seconds, enforced by Ansible's
`timeout` keyword). A failed phase **stops the sequence** on that host: carrying on would apply
hardening to a missing installation and bury the root cause under a second failure.

The invariant, and it is not cosmetic: **a phase's bound must fire before the Job's deadline**
(`Enrollment.spec.policy.timeout` -> `activeDeadlineSeconds`).

What it guarantees, precisely: the secrets file is written once for the whole sequence (later phases
need it) and destroyed in an `always` block, which therefore covers a phase failure. If the **Job**
expires instead, the pod is killed outright: the `always` block does not run, nothing names the
failing phase, and **the secrets file survives as `0600` on the machine**, passwords in clear text, on
a gateway that may sit in a DMZ.

The concrete trap, documented in `defaults/main.yml` next to the value: a phase **without** `timeout`
inherits a bound derived from the Job's **total** budget minus a margin (`internal/exec`), not from what
is left. With a 45 min budget, an unbounded phase inherits 44 min, so if the previous phase used its
nominal 30 min and this one hangs, the bound never fires. The role's own default (`timeout: 3600`) is
deliberately **above any Job budget**: it is **not** used for rondo-driven runs, where the verb always
resolves the bound.

Writing rule for a component author: **bound every phase**, and keep their sum strictly under the Job's
budget. As `demo-mft` does: 30 m + 5 m + 3 m + 5 m = 43 m under the 45 min of its `Enrollment`.

## Why Ansible and not a hand-rolled `ssh`

The Job runs in the management cluster and reaches the machines over SSH. Ansible brings idempotence,
per-host error handling, per-group parallelism and modules (`yum_repository`, `get_url`, `unarchive`)
that we would rewrite badly. And it requires **nothing** on the target machine besides `python3`,
installed by cloud-init.

`site.yml` plays the role on the `rondo_machines` group, with `serial: 4` by default: enough for a
multi-VM component, few enough that the `kubectl logs` output stays readable and the Artifactory
repository is not hammered by N simultaneous downloads.

> Note for components whose instructions say "no Ansible control node": that constraint comes from a
> Windows jump host, not from the target. Here the control node is an ephemeral pod of the management
> cluster, so the constraint goes away, and with it the manual transfer of multi-gigabyte bundles.

## Layout

```
ansible/
├── ansible.cfg
├── site.yml                        # plays bundle_install on rondo_machines
└── roles/
    └── bundle_install/
        ├── defaults/main.yml       # THE role's input contract, commented
        ├── tasks/
        │   ├── main.yml            # the order of the steps
        │   ├── rpm_repos.yml
        │   ├── artifacts.yml
        │   ├── installer.yml       # config + secrets + loop over the phases + shred
        │   └── installer_phase.yml # ONE phase: run, report the output, fail
        └── templates/
            └── <format>-config.j2  # rendering of spec.vm.config: shell, env, yaml, json
```

`installer_phase.yml` is a file rather than a looped `command` task because a phase must run, **then**
report its output, **then** fail: several tasks, hence an `include_tasks`. It **decides nothing**: the
defaults, the inherited time bound and the co-location of executables are decided in `internal/exec`,
where they are tested.

## Input variables

The `vm-install` Job passes them as `--extra-vars` from a JSON file derived from the `Component` and the
`Target`; the component author never writes them by hand. See `defaults/main.yml` for the full
contract, commented key by key.
