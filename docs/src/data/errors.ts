// Messages quoted from the code; <x> marks a variable part.

export type Where = 'CLI' | 'Enrollment' | 'Health' | 'Target' | 'Job';
export type Entry = { where: Where; msg: string; cause: string; fix: string };

export const errors: Entry[] = [
  // CLI
  { where: 'CLI', msg: 'workstation prerequisites missing, install them before running again', cause: 'A tool is missing from the PATH; the list says what each one is for.', fix: 'Install bash, openssh-clients, sshpass (EPEL, or mirrored to Artifactory).' },
  { where: 'CLI', msg: 'invalid manifest: <detail>', cause: 'rondo.yaml fails validation (unknown key, invalid IP, even number of control planes…).', fix: 'Fix the key quoted; see the rondo.yaml reference.' },
  { where: 'CLI', msg: '<rondo.yaml> declares environment "<x>", the selected environment is "<y>"', cause: "The file's environmentName does not match the chosen environment.", fix: 'rondo env select <x>, or --env <x>, or fix environmentName.' },
  { where: 'CLI', msg: 'unknown environment "<n>", create it with: rondo init --env <n>', cause: 'No ~/.rondo/envs/<n>/rondo.yaml.', fix: 'rondo init --env <n>.' },
  { where: 'CLI', msg: "folder '/<dc>/vm/<folder>' not found", cause: 'vmFolder does not exist in vCenter (/vm/ is the VM root, invisible in the UI).', fix: 'Create the folder, or fix targets.vsphere[].vmFolder.' },
  { where: 'CLI', msg: 'cloud-init timeout on <ip> after 10m0s', cause: 'The VM has no IP, or the template has no cloud-init, or its interface is not ens32.', fix: 'VM console in vCenter; check vmNetwork, serverNodes[].ip, the template.' },
  { where: 'CLI', msg: 'The plan for <x> DESTROYS <resources>. Apply? (y/N)', cause: 'An rondo.yaml change recreates VMs (size, template, IP…).', fix: 'Read the plan; answer N if it is not intended. --yes accepts in advance.' },
  { where: 'CLI', msg: 'cancelled', cause: 'A confirmation received something other than y.', fix: 'Run again, or --yes.' },
  { where: 'CLI', msg: '401 from Artifactory', cause: 'Expired password or token.', fix: 'secrets.artifactoryPassword, secrets.artifactoryMirrorToken, then rondo deploy.' },
  { where: 'CLI', msg: 'unknown placeholders: ${<X>}; declare them in …', cause: 'The tree uses an unknown site token, or one whose value is empty in rondo.yaml (e.g. ${ARTIFACTORY_RPM_REPO} without rpmRepository).', fix: 'Fill in the rondo.yaml key, or pass the value through ${target.values.*}.' },
  { where: 'CLI', msg: 'SHA-256 checksum mismatch: want <a>, got <b>', cause: 'Corrupt archive, or republished under the same version.', fix: 'Republish under a new version; empty the relevant ~/.rondo/cache/… directory.' },
  { where: 'CLI', msg: '… refused, a tree only carries Components and Enrollments', cause: 'The published tree contains another kind (a Target, a Secret…).', fix: 'Remove the object from the tree; Targets come from rondo.yaml, secrets from secrets.components.' },
  { where: 'CLI', msg: 'Rancher token in state is invalid (Rancher reprovisioned?), regenerating', cause: 'Warning: the token in rondo.state.json no longer works.', fix: 'Nothing, unless the admin login then fails: secrets.rancherBootstrapPassword must be the current admin password.' },
  { where: 'CLI', msg: 'cert-manager CA extraction (cluster-ca-keypair missing?)', cause: "The management cluster's PKI is not ready.", fix: 'Run rondo deploy again.' },
  { where: 'CLI', msg: 'unknown rke2 target "<n>" (declared: …)', cause: 'Name missing from targets.downstreams.', fix: 'Fix the name or rondo.yaml.' },

  // Enrollment
  { where: 'Enrollment', msg: 'no Target has the name or role "<x>"', cause: 'No target has this name or role.', fix: 'Add the role in rondo.yaml, then rondo deploy or rondo deploy downstreams.' },
  { where: 'Enrollment', msg: '"<x>" matches several Targets (<a b>), an Enrollment targets exactly one', cause: 'Two targets carry this role.', fix: 'Remove the role from one of them.' },
  { where: 'Enrollment', msg: '"<x>" resolved to <a> and now resolves to <b>: delete and recreate the Enrollment to move the component', cause: 'The role moved to another target; the resolved target is fixed.', fix: 'kubectl -n rondo-system delete enrollment <name> (uninstalls from a), then rondo component install.' },
  { where: 'Enrollment', msg: 'Component "<x>" not found', cause: 'The Enrollment references a missing Component.', fix: 'rondo component install <tree>; check spec.component.' },
  { where: 'Enrollment', msg: 'target <a> does not accept a component of archetype "<chart|vm>"', cause: 'A chart targets a vSphere Target, or machines a Kubernetes Target.', fix: 'Target a target of the right type.' },
  { where: 'Enrollment', msg: 'target <a> not ready, see `kubectl describe target <a>`', cause: 'The Target is Ready=False (kubeconfig missing or invalid).', fix: "Read the Target's message; run rondo deploy downstreams <a> again." },
  { where: 'Enrollment', msg: 'waiting for [<dependencies>] on <a>', cause: 'A dependsOn is not Ready on the same target.', fix: "Look at the dependency's Enrollment." },
  { where: 'Enrollment', msg: '10 consecutive failures, retries stopped so as not to flood the cluster with Jobs', cause: 'The Job fails in a loop: Stalled.', fix: 'Read kubectl logs job/<last>, fix, then change the spec or reset status.consecutiveFailures to 0.' },
  { where: 'Enrollment', msg: 'unknown substitution token(s): target.values.<k>, available: …', cause: "A Target value is missing. For a chart, the error is in the controller's logs; for machines, in the Enrollment's message.", fix: "Add <k> to the target's componentValues in rondo.yaml, then rondo deploy (downstreams)." },
  { where: 'Enrollment', msg: 'mutual-exclusion lease LOST to "<x>" while Job <job> is still …', cause: 'Another controller took the lease during a Job (LeaseLost).', fix: 'Check that a single rondo-controller pod runs; follow the running Job.' },
  { where: 'Enrollment', msg: 'Job not found, outcome unknown (TTL expired?)', cause: 'The referenced Job is gone (deleted, or 24 h TTL elapsed): attempt "Aborted".', fix: 'Nothing: the Enrollment is re-evaluated, a new Job starts if needed.' },

  // Health
  { where: 'Health', msg: 'applied, health not evaluated yet', cause: 'rondo-probe has not run yet (every 30 s).', fix: 'Wait.' },
  { where: 'Health', msg: 'applied but Progressing: <Kind ns/name>…', cause: 'The Job succeeded but a delivered object is not ready.', fix: 'kubectl get pods on the target; the message names the object.' },
  { where: 'Health', msg: 'object missing from the cluster (rejected by a webhook? deleted by a controller?)', cause: 'An inventory object no longer exists: Degraded.', fix: 'Find out who deleted it; apply again (clear appliedValuesHash).' },
  { where: 'Health', msg: 'kind <G/Kind> not modelled: declare it in the Component\'s spec.health, …', cause: 'rondo-probe cannot judge this kind, which has no Ready condition: Unknown.', fix: 'Add a spec.health rule {group, kind, condition} to the Component.' },
  { where: 'Health', msg: 'target unreachable: … / cache disconnected from the target: …', cause: 'rondo-probe no longer reaches the cluster: Unknown.', fix: "Network, the Target's kubeconfig (rondo deploy downstreams <t>)." },

  // Target
  { where: 'Target', msg: 'Secret "rondo-kubeconfig-<t>" not found in rondo-system (did `rondo deploy downstreams` succeed?)', cause: 'The rke2 cluster has not finished being created.', fix: 'rondo deploy downstreams <t>.' },
  { where: 'Target', msg: 'kubernetes.server (<a>) differs from the kubeconfig endpoint (<b>): …', cause: 'The kubeconfig changed without the Target.', fix: 'rondo deploy downstreams <t> refreshes both.' },
  { where: 'Target', msg: 'placement setting(s) missing from ConfigMap "rondo-vsphere-<n>": …', cause: 'A value required by terraform/vm is missing.', fix: 'Complete targets.vsphere[] in rondo.yaml, then rondo deploy.' },
  { where: 'Target', msg: 'Secret "rondo-vsphere-<n>" not found in rondo-system (does `rondo deploy` project the vSphere credentials?)', cause: 'rondo deploy has not succeeded since the target was added.', fix: 'rondo deploy.' },

  // Job
  { where: 'Job', msg: 'helm: timed out waiting for the condition / UPGRADE FAILED', cause: 'The chart does not become ready before policy.timeout − 30 s; --atomic rolled the release back.', fix: 'kubectl logs job/<job>, then the pods on the target; raise spec.policy.timeout if the install is slow.' },
  { where: 'Job', msg: 'network_mirror: 401/403 … Regenerate it with `rondo deploy`', cause: 'The tofu providers token (Secret rondo-tofurc) is invalid.', fix: 'rondo deploy, or set secrets.artifactoryMirrorToken.' },
  { where: 'Job', msg: 'uninstall failed, the finalizer is released anyway, the helm release may remain', cause: "The helm-uninstall Job failed; the Enrollment disappears anyway (controller log).", fix: 'helm uninstall <release> -n <ns> by hand on the target.' },
];
