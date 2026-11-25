import type {
  AttemptView,
  ComponentView,
  EnrollmentView,
  JobView,
  ServiceView,
  Summary,
  TargetView,
} from "../src/lib/types.ts"

const VERSION = "edge-main-9c481a63"
const REGISTRY = "registry.example/rondo-docker-local"

function ago(minutes: number): string {
  return new Date(Date.now() - minutes * 60_000).toISOString().replace(/\.\d{3}Z$/, "Z")
}

function attempt(
  jobName: string,
  result: string,
  version: string,
  start: number,
  end: number,
  message?: string,
): AttemptView {
  return { jobName, result, version, startedAt: ago(start), finishedAt: ago(end), message }
}

function fnv(s: string): string {
  let h = 2166136261
  for (const c of s) h = Math.imul(h ^ c.charCodeAt(0), 16777619)
  return (h >>> 0).toString(16).padStart(8, "0")
}

function hash(s: string): string {
  return (fnv(s) + fnv(`${s}#`)).slice(0, 10)
}

// Same naming as internal/jobs: <kind>-<owner>-<verb>-<fingerprint>, owner truncated to fit 52 chars.
function jobName(owner: string, verb: string, salt = ""): string {
  const kind = "enrollment"
  const fp = hash(kind + owner + verb + salt)
  const v = verb.replaceAll("-", "")
  const base = `${kind}-${owner}-${v}-${fp}`
  if (base.length <= 52) return base
  return `${kind}-${owner.slice(0, Math.max(1, 52 - kind.length - v.length - fp.length - 3))}-${v}-${fp}`
}

const targets: TargetView[] = [
  {
    name: "management",
    type: "kubernetes",
    inCluster: true,
    ready: "True",
    values: { clusterDomain: "mgmt.rondo.example", imageRegistry: REGISTRY, ingressIP: "10.20.0.10" },
  },
  {
    name: "rke2-a",
    type: "kubernetes",
    inCluster: false,
    server: "https://rancher.mgmt.rondo.example/k8s/clusters/c-m-7x2kq9vd",
    ready: "True",
    roles: ["monitoring"],
    values: { ingressIP: "10.20.1.10", storageClass: "local-path" },
  },
  {
    name: "dc1",
    type: "vsphere",
    inCluster: false,
    ready: "True",
    roles: ["dmz"],
    values: { vmTemplate: "rhel9-cloudinit", "vmIPs.demo-mft.production": '["10.20.2.10"]' },
  },
]

interface Decl {
  name: string
  chart?: string
  version?: string
  groups?: string[]
  namespace?: string
  dependsOn?: string[]
}

const declared: Decl[] = [
  { name: "cert-manager", chart: "cert-manager", version: "v1.18.2", namespace: "cert-manager" },
  { name: "metallb", chart: "metallb", version: "0.15.2", namespace: "metallb-system" },
  { name: "traefik", chart: "traefik", version: "41.0.2", namespace: "traefik", dependsOn: ["metallb"] },
  { name: "grafana", chart: "grafana", version: "10.1.2", namespace: "monitoring", dependsOn: ["traefik"] },
  { name: "demo-mft", groups: ["production", "administration"] },
]

const GRAFANA_JOB = jobName("grafana", "helm-apply", "3")
const GRAFANA_OLD_JOB = jobName("grafana", "helm-apply", "2")
const MFT_APPLY_JOB = jobName("demo-mft", "vm-apply")
const MFT_INSTALL_JOB = jobName("demo-mft", "vm-install")

export const LOG_FILES: Record<string, string> = {
  [GRAFANA_JOB]: "grafana-helm-apply.log",
  [MFT_INSTALL_JOB]: "demo-mft-vm-install.log",
  "rondo-probe-6b8f9c7d54-x2k9p": "rondo-probe-crashloop.log",
  "rondo-controller-7d9c6b5f48-abcde": "rondo-controller.log",
}

export const EXPIRED_JOBS = new Set([GRAFANA_OLD_JOB])
export const GROWING_LOGS = new Set([MFT_INSTALL_JOB])

const grafanaFailure =
  "helm upgrade --install grafana: UPGRADE FAILED: context deadline exceeded " +
  "(Deployment monitoring/grafana: 0/1 replica available, ImagePullBackOff)"

function healthy(name: string, component: string, target: string, version: string, objects: number): EnrollmentView {
  const job = jobName(name, "helm-apply")
  return {
    name,
    component,
    target,
    ready: "True",
    health: "Healthy",
    chartVersion: version,
    valuesHash: hash(name).repeat(7).slice(0, 64),
    inventoryCount: objects,
    attempts: 1,
    consecutiveFailures: 0,
    lastAttempt: attempt(job, "Succeeded", version, 1442, 1440),
    history: [attempt(job, "Succeeded", version, 1442, 1440)],
  }
}

function enrollments(): EnrollmentView[] {
  const out: EnrollmentView[] = [
    healthy("cert-manager-rke2-a", "cert-manager", "rke2-a", "v1.18.2", 47),
    healthy("metallb-rke2-a", "metallb", "rke2-a", "0.15.2", 31),
    healthy("traefik-rke2-a", "traefik", "rke2-a", "41.0.2", 12),
    {
      name: "grafana",
      component: "grafana",
      target: "rke2-a",
      ready: "False",
      message: `3 consecutive failures, next attempt in 4 min: ${grafanaFailure}`,
      health: "Degraded",
      healthMessage: "Deployment monitoring/grafana: 0/1 replica available",
      chartVersion: "10.0.0",
      valuesHash: "9a8b7c6d5e4f30211203f4e5d6c7b8a99a8b7c6d5e4f30211203f4e5d6c7b8a9",
      inventoryCount: 14,
      attempts: 3,
      consecutiveFailures: 3,
      lastAttempt: attempt(GRAFANA_JOB, "Failed", "10.1.2", 18, 13, grafanaFailure),
      history: [
        attempt(GRAFANA_JOB, "Failed", "10.1.2", 18, 13, grafanaFailure),
        attempt(GRAFANA_OLD_JOB, "Failed", "10.1.2", 58, 53, grafanaFailure),
        attempt(jobName("grafana", "helm-apply", "0"), "Succeeded", "10.0.0", 1400, 1397),
      ],
    },
    {
      name: "demo-mft",
      component: "demo-mft",
      target: "dc1",
      phase: "Install",
      ready: "False",
      message: `installation running (Job ${MFT_INSTALL_JOB})`,
      inventoryCount: 0,
      attempts: 2,
      consecutiveFailures: 0,
      lastAttempt: attempt(MFT_APPLY_JOB, "Succeeded", "2.3.0", 28, 7),
      history: [attempt(MFT_APPLY_JOB, "Succeeded", "2.3.0", 28, 7)],
    },
  ]
  return out.sort((a, b) => a.name.localeCompare(b.name))
}

function components(): ComponentView[] {
  const enrs = enrollments()
  return declared
    .map((d) => ({
      name: d.name,
      archetype: d.groups ? "vm" : "chart",
      chart: d.chart ? `oci://registry.example/rondo-helm-local/${d.chart}` : undefined,
      version: d.version,
      namespace: d.namespace,
      groups: d.groups,
      dependsOn: d.dependsOn,
      enrollments: enrs
        .filter((e) => e.component === d.name)
        .map((e) => ({ name: e.name, ready: e.ready, target: e.target })),
    }))
    .sort((a, b) => a.name.localeCompare(b.name))
}

function job(name: string, verb: string, owner: string, state: string, start?: number, end?: number): JobView {
  return {
    name,
    verb,
    owner: `Enrollment/${owner}`,
    state,
    active: state === "Running" ? 1 : 0,
    succeeded: state === "Succeeded" ? 1 : 0,
    failed: state === "Failed" ? 1 : 0,
    startedAt: start === undefined ? undefined : ago(start),
    endedAt: end === undefined ? undefined : ago(end),
  }
}

export function jobs(): JobView[] {
  const list = [
    job(MFT_INSTALL_JOB, "vm-install", "demo-mft", "Running", 6),
    job(GRAFANA_JOB, "helm-apply", "grafana", "Failed", 18),
    job(MFT_APPLY_JOB, "vm-apply", "demo-mft", "Succeeded", 28, 7),
    job(GRAFANA_OLD_JOB, "helm-apply", "grafana", "Failed", 58),
  ]
  return list.sort((a, b) => (b.startedAt ?? "").localeCompare(a.startedAt ?? "") || a.name.localeCompare(b.name))
}

export function services(): ServiceView[] {
  const image = (name: string) => `${REGISTRY}/${name}:${VERSION}`
  const pod = (name: string, node: string, started: number) => ({
    name,
    phase: "Running",
    ready: true,
    restarts: 0,
    node,
    startedAt: ago(started),
  })
  return [
    {
      name: "rondo-controller",
      image: image("rondo-controller"),
      version: VERSION,
      desired: 1,
      ready: 1,
      updated: 1,
      available: 1,
      state: "Healthy",
      pods: [pod("rondo-controller-7d9c6b5f48-abcde", "k3s-server-1", 1440)],
    },
    {
      name: "rondo-dashboard",
      image: image("rondo-toolbox"),
      version: VERSION,
      desired: 1,
      ready: 1,
      updated: 1,
      available: 1,
      state: "Healthy",
      pods: [pod("rondo-dashboard-5f7b8c9d6e-h4j2k", "k3s-server-2", 96)],
    },
    {
      name: "rondo-probe",
      image: image("rondo-toolbox"),
      version: VERSION,
      desired: 2,
      ready: 1,
      updated: 2,
      available: 1,
      state: "Degraded",
      message: "1/2 pods ready: rondo-probe-6b8f9c7d54-x2k9p in CrashLoopBackOff",
      pods: [
        pod("rondo-probe-6b8f9c7d54-m7n5v", "k3s-server-1", 1440),
        { ...pod("rondo-probe-6b8f9c7d54-x2k9p", "k3s-server-2", 52), ready: false, restarts: 14, reason: "CrashLoopBackOff" },
      ],
    },
  ]
}

export function overview(): Summary[] {
  const summary = (kind: string, rows: Summary["rows"]): Summary => ({
    kind,
    total: rows.length,
    ready: rows.filter((r) => r.ready === "True").length,
    rows: [...rows].sort((a, b) => a.name.localeCompare(b.name)),
  })
  return [
    summary("Target", targets.map((t) => ({ name: t.name, ready: t.ready, message: t.message }))),
    summary("Component", declared.map((d) => ({ name: d.name, ready: "True", phase: d.groups ? "vm" : "chart" }))),
    summary(
      "Enrollment",
      enrollments().map((e) => ({ name: e.name, ready: e.ready, phase: e.phase || e.health, message: e.message })),
    ),
  ]
}

export const api = {
  overview,
  components,
  enrollments,
  targets: () => targets,
  jobs,
  services,
}
