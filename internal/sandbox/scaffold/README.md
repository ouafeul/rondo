# __NAME__

An RONDO component. `platform/` says what to install (Components) and where (Enrollments, by role);
`rondo-sandbox.yaml` describes the sandbox site, with the keys of `rondo.yaml`, and what the
component's CI publishes.

| Path | Purpose |
|---|---|
| `platform/components/`, `platform/enrollments/` | the tree the site installs |
| `apps/` | the first-party charts, which the CI publishes (archetype A) |
| `build/` | the images, which the CI builds and pushes |
| `manifests/dependencies.yaml` | what the site mirrors: upstream charts, files, RPM repositories |
| `vm/payload/` | the machine's archive and its phases (archetype B) |
| `rondo-sandbox.yaml` | the sandbox site, what the CI publishes, the workstation ports |

## Try it on the workstation

```bash
rondo-sandbox deploy                 # the management cluster (k3d), RONDO, the vSphere Targets
rondo-sandbox deploy downstreams     # one cluster per downstream in rondo-sandbox.yaml
rondo-sandbox component install      # validates, publishes, installs, waits for RONDO's verdict
rondo-sandbox status                 # Enrollments, URLs, machines, workstation ports
```

The `ports` of `rondo-sandbox.yaml` open the pages on `http://localhost:<port>`. After a
change, `rondo-sandbox component install` replays what changed; `rondo-sandbox check`
validates without deploying anything; `rondo-sandbox destroy` frees the workstation.

## On a site

The CI publishes the charts of `apps/`, the images of `build/` and the artifacts of `vm/`; the
site declares its Targets and the component's Secrets in `rondo.yaml`, then runs
`rondo component install __NAME__`. Before publishing:

```bash
rondo component lint platform --crds <rondo>/manifests/crds --deps manifests/dependencies.yaml
```
