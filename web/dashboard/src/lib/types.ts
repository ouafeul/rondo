export interface Row {
  name: string
  ready: string
  phase?: string
  message?: string
}

export interface Summary {
  kind: string
  total: number
  ready: number
  rows: Row[]
}

export interface PlacementView {
  name: string
  ready: string
  target: string
}

export interface ComponentView {
  name: string
  archetype: string
  chart?: string
  version?: string
  namespace?: string
  groups?: string[]
  dependsOn?: string[]
  enrollments: PlacementView[]
}

export interface AttemptView {
  jobName: string
  result: string
  version?: string
  message?: string
  startedAt?: string
  finishedAt?: string
}

export interface EnrollmentView {
  name: string
  component: string
  target: string
  phase?: string
  ready: string
  message?: string
  health?: string
  healthMessage?: string
  chartVersion?: string
  valuesHash?: string
  inventoryCount: number
  attempts: number
  consecutiveFailures: number
  lastAttempt?: AttemptView
  history?: AttemptView[]
}

export interface TargetView {
  name: string
  type: string
  inCluster: boolean
  server?: string
  ready: string
  message?: string
  roles?: string[]
  values?: Record<string, string>
}

export interface JobView {
  name: string
  verb?: string
  owner?: string
  state: string
  active: number
  succeeded: number
  failed: number
  startedAt?: string
  endedAt?: string
}

export interface PodView {
  name: string
  phase: string
  ready: boolean
  restarts: number
  node?: string
  startedAt?: string
  reason?: string
}

export interface ServiceView {
  name: string
  image: string
  version: string
  desired: number
  ready: number
  updated: number
  available: number
  state: string
  message?: string
  pods: PodView[]
}
