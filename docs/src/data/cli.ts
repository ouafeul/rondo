// What each command runs, in order. Step names are the CLI's own (cmd/rondo/plans.go,
// internal/steps): an error is printed as "<step>: <cause>".

export type Tool = 'workstation' | 'tofu' | 'ssh' | 'helm' | 'kubectl' | 'http';

export type Step = {
  name: string;
  tool: Tool;
  does: string;
  writes?: string[];
  // what --dry-run does instead; absent => skipped entirely or only logged
  dry?: string;
};

export type Flag = { name: string; def?: string; help: string };

export type Command = {
  id: string;
  usage: string;
  summary: string;
  confirm?: string;
  steps?: Step[];
  flags?: Flag[];
  notes?: string[];
  group: 'cycle' | 'component' | 'env' | 'misc';
};

const tools: Step = {
  name: 'tool download',
  tool: 'http',
  does: 'tofu, helm, helmfile, kubectl, k9s, yq and the helm-diff plugin into ~/.rondo/bin, at the bundle versions (manifests/dependencies.yaml), each checked against the sha256 pinned there and the <file>.sha256 the mirror serves, a mismatch refused. A tool already present is not downloaded again.',
  dry: 'prints the URLs',
};
const registry: Step = {
  name: 'OpenTofu registry setup',
  tool: 'http',
  does: '.tofurc pointing to the providers mirror (network_mirror or OCI), with its credentials: secrets.mirrorToken, a one-hour token minted with preset artifactory, or the service account on an OCI mirror; the state backend (http or s3).',
  writes: ['<state>/.tofurc'],
  dry: 'no token, no .tofurc',
};
const rancherToken: Step = {
  name: 'Rancher API token',
  tool: 'http',
  does: 'Exports the cluster CA and validates the token kept in rondo.state.json; if it is invalid, logs in as admin with secrets.rancherBootstrapPassword and creates one (ttl 0).',
  writes: ['<state>/rondo.state.json', '<state>/certs/rancher-ca.pem'],
};

export const commands: Command[] = [
  {
    id: 'deploy',
    group: 'cycle',
    usage: 'rondo deploy',
    summary: "The management cluster, the operator, the site's Targets and RONDO's Components. Idempotent.",
    steps: [
      { name: 'host prerequisites preflight', tool: 'workstation', does: 'bash, ssh, scp, sshpass, ssh-keygen must be in the PATH; the list of missing ones says what each is for.', dry: 'only warns' },
      tools,
      registry,
      { name: 'k3s VM provisioning (tofu)', tool: 'tofu', does: 'terraform/k3s: folder <vmFolder>/<env>/MANAGEMENT and the 3 VMs, state <env>/k3s. A plan that destroys asks for confirmation.', dry: 'plan not computed' },
      { name: 'cloud-init wait', tool: 'ssh', does: 'Waits for /var/lib/cloud/instance/cloud-init-complete on each VM: 60 attempts × 10 s.' },
      { name: 'k3s HA cluster formation', tool: 'ssh', does: "scripts/k3s-setup.sh: k3s in HA (embedded etcd), servicelb disabled, k3s's Traefik kept." },
      { name: 'kubeconfig install', tool: 'workstation', does: 'Merges the kubeconfig into ~/.kube/config, context rondo (or rondo-<env>), current if the environment is selected.', writes: ['<state>/kubeconfig.yaml', '~/.kube/config'] },
      { name: 'bootstrap manifest rendering', tool: 'workstation', does: 'PKI (ClusterIssuers selfsigned-bootstrap and cluster-ca-issuer, CA "RONDO <env> CA", Rancher certificate) and the MetalLB pool of the ingress IP.' },
      { name: 'Helm chart deployment', tool: 'helm', does: 'helmfile sync: MetalLB, cert-manager (then the PKI), Rancher (rancher.<base>).' },
      { name: 'operator install', tool: 'kubectl', does: 'CRDs (awaited Established), rondo-system namespace, RBAC, Deployments rondo-controller, rondo-probe, rondo-dashboard, Ingress rondo.<base>, NetworkPolicies. Images at the CLI version.' },
      { name: 'platform credentials', tool: 'kubectl', does: 'Mirror account and its CA for the Jobs.', writes: ['Secret rondo-credentials', 'ConfigMap mirror-ca'] },
      { name: 'network_mirror token', tool: 'http', does: "Long-lived token for the Jobs' tofu providers: secrets.mirrorToken, else a token issued and rotated on each deploy (preset artifactory) or none (preset generic).", writes: ['Secret rondo-tofurc'] },
      { name: 'internal DNS resolution', tool: 'kubectl', does: "Management CoreDNS: rancher|rondo.<base> → management ingress IP; *.<target or role>.<base> → each downstream's ingress IP.", writes: ['ConfigMap kube-system/coredns-custom'] },
      { name: 'Targets and site values', tool: 'kubectl', does: 'Target management, one Target per targets.vsphere entry, component Secrets, machine SSH key (generated once).', writes: ['Target management', 'Target <vsphere>', 'Secret/ConfigMap rondo-vsphere-<n>', 'Secret rondo-platform-values', 'Secret rondo-vm-ssh', 'Secret <secrets.components.x>'] },
      { name: 'operator startup wait', tool: 'kubectl', does: 'rollout status of the three Deployments, 10 min at most.' },
      { name: 'RONDO components', tool: 'kubectl', does: 'Applies the platform/components tree: the Components of the downstreams base layer. No Enrollment.', writes: ['Components', 'ConfigMap rondo-component-rondo'], dry: 'lists what would be applied' },
    ],
  },
  {
    id: 'deploy-downstreams',
    group: 'cycle',
    usage: 'rondo deploy downstreams [target...]',
    summary: 'One rke2 cluster per targets.downstreams entry (all, or those named), its Target, and the Enrollments of its base layer.',
    steps: [
      { name: 'host prerequisites preflight', tool: 'workstation', does: 'Only ssh-keygen is required.', dry: 'only warns' },
      tools,
      registry,
      rancherToken,
      { name: 'Rancher server-url setting', tool: 'kubectl', does: 'Sets https://rancher.<base> if empty; only warns if it differs.' },
      { name: 'regcred secret (fleet-default)', tool: 'kubectl', does: 'Registry credentials for the rke2 nodes.', writes: ['Secret fleet-default/regcred'] },
      { name: 'cluster <target>', tool: 'tofu', does: 'Per target: terraform/rke2 (Rancher creates the VMs in <vmFolder>/<env>/<target> and installs rke2), then the kubeconfig, the Target, and the Enrollments <component>-<target>.', writes: ['Secret rondo-kubeconfig-<t>', 'Target <t>', 'Enrollments <component>-<t>', 'ConfigMap rondo-component-base-<t>'], dry: 'plan not computed, nothing written' },
    ],
    notes: ['The controller takes over: it installs the base layer on the new cluster, in dependsOn order.'],
  },
  {
    id: 'component-install',
    group: 'component',
    usage: 'rondo component install <name>',
    summary: "Downloads a component's published tree, checks its checksum, replaces the site tokens and applies it.",
    flags: [
      { name: '--version', def: '""', help: 'pins a published version (empty => the channel version)' },
      { name: '--channel', def: 'latest', help: 'pointer <name>/platform/<channel>.json' },
      { name: '--from', def: '""', help: 'installs a local tree (version "local")' },
    ],
    steps: [
      { name: 'resolution', tool: 'http', does: '<generic>/<name>/platform/<channel>.json, or --version, or --from.' },
      { name: 'download', tool: 'http', does: 'platform-<version>.tar.gz and its .sha256, checked, cached in ~/.rondo/cache/components/<name>/<version>.', dry: 'downloads anyway' },
      { name: 'site tokens', tool: 'workstation', does: '${OCI_REGISTRY}, ${INGRESS_BASE_DOMAIN}… replaced; an unknown ${UPPERCASE} token refuses the tree.' },
      { name: 'apply', tool: 'kubectl', does: 'Only Components and Enrollments are allowed. Server-side apply (field manager rondo-cli), then deletion of what the previous version applied and this one no longer has.', writes: ['Components', 'Enrollments', 'ConfigMap rondo-component-<name>'], dry: 'lists what would be applied' },
    ],
  },
  {
    id: 'component-uninstall',
    group: 'component',
    usage: 'rondo component uninstall <name>',
    summary: 'Deletes everything the tree applied. Each Enrollment uninstalls its workload from its target before disappearing.',
    confirm: 'Uninstall <name> from all its Targets',
    notes: ["Deletes the Enrollments, waits until they are gone (30 min at most), then the Components: an Enrollment's finalizer needs its Component to uninstall. If the wait fails, nothing else is deleted: run the command again."],
  },
  {
    id: 'destroy-downstreams',
    group: 'cycle',
    usage: 'rondo destroy downstreams [target...]',
    summary: 'Removes the base layer, destroys the rke2 cluster and deletes its Target.',
    confirm: 'Confirm destroying <targets>',
    steps: [
      tools,
      registry,
      rancherToken,
      { name: 'destroying <target>', tool: 'tofu', does: 'Deletes the base-layer Enrollments (without waiting), tofu destroy of the cluster, then the Target and rondo-kubeconfig-<t>.' },
    ],
    notes: ["Uninstall the target's components first: the CLI does not wait for finalizers before tofu destroy."],
  },
  {
    id: 'destroy',
    group: 'cycle',
    usage: 'rondo destroy',
    summary: 'Destroys the management cluster (tofu destroy of terraform/k3s).',
    confirm: 'Confirm destroying the management cluster',
    notes: ['Touches neither the downstreams nor the ~/.kube/config context: destroy the downstreams first.'],
  },
  {
    id: 'init',
    group: 'env',
    usage: 'rondo init [--env <e>] [--force]',
    summary: 'Creates ~/.rondo and writes an rondo.yaml to fill in (environmentName set). Refuses to overwrite without --force.',
    flags: [{ name: '--force', def: 'false', help: 'overwrites an existing rondo.yaml' }],
  },
  {
    id: 'env',
    group: 'env',
    usage: 'rondo env list | select <name>',
    summary: 'Lists the environments (* = selected), or selects the one for the following commands and switches the kubectl context rondo[-<env>].',
  },
  {
    id: 'lint',
    group: 'component',
    usage: 'rondo component lint [directory] [--crds dir] [--deps file]',
    summary: "Validates a component tree against the CRDs before publishing. Hidden command, used by components' CI.",
    flags: [
      { name: '--crds', def: '$RONDO_CRDS_DIR or manifests/crds', help: 'reference CRDs' },
      { name: '--deps', help: 'dependencies.yaml: checks that charts, artifacts and RPM repositories are mirrored' },
    ],
  },
  {
    id: 'tui',
    group: 'misc',
    usage: 'rondo tui [--kube-context <ctx>]',
    summary: 'Terminal UI: the dashboard views (components, enrollments, targets, Jobs and logs, services, installed components) and the CLI commands, run as subprocesses.',
    flags: [{ name: '--kube-context', def: '""', help: 'kubectl context of the management cluster (empty => rondo[-<env>])' }],
    notes: [
      "Reads the cluster through its dashboard, read-only: kubectl port-forward svc/rondo-dashboard with the environment's kubectl context, and kubectl get for the installed components. The kubectl is rondo's (~/.rondo/bin), else the one in the PATH; without a context, only the commands remain.",
      'Each command is the CLI itself (rondo deploy, rondo component install…): the console shows its steps, its confirmations (y/N) appear in the interface, x interrupts it like Ctrl+C.',
      'D and S toggle --dry-run and --skip-download; --env, --config, --log-level, --iac-version and --iac-channel are passed on, never --yes.',
      'One command at a time. Quitting during a command interrupts it and waits for it to end.',
      'e (Environments) or E (anywhere) edits rondo.yaml: validated on each keystroke as rondo will, the error pointed at by its line and path (ctrl+l goes there); ctrl+s saves, asking first for an invalid manifest or a file changed in the meantime; ctrl+o hands over to $VISUAL or $EDITOR. After rondo init, the editor opens on the manifest to fill in.',
    ],
  },
  { id: 'version', group: 'misc', usage: 'rondo version', summary: 'rondo <version> (commit <sha>).' },
];

export const globalFlags: Flag[] = [
  { name: '-c, --config', def: 'rondo.yaml', help: 'manifest path' },
  { name: '--env', def: '""', help: 'targeted environment; empty => the one from rondo env select' },
  { name: '--dry-run', def: 'false', help: 'shows the plan without changing anything; implies --yes' },
  { name: '--yes', def: 'false', help: 'asks for no confirmation' },
  { name: '--skip-download', def: 'false', help: 'reuses the tools already downloaded' },
  { name: '--iac-version', def: '""', help: 'pins the IaC bundle version' },
  { name: '--iac-channel', def: '""', help: 'stable (default) or edge-<branch>; takes precedence over artifacts.releaseChannel' },
  { name: '--log-level', def: 'info', help: 'trace|debug|info|warn|error' },
  { name: '--debug', def: 'false', help: '= --log-level debug' },
];
