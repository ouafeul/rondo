import type { BadgeVariant } from "@/components/ui/badge"

const TONES: Record<string, BadgeVariant> = {
  True: "ok",
  Healthy: "ok",
  Succeeded: "ok",
  False: "bad",
  Degraded: "bad",
  Down: "bad",
  Failed: "bad",
  TimedOut: "bad",
  CrashLoopBackOff: "bad",
  ImagePullBackOff: "bad",
  ErrImagePull: "bad",
  OOMKilled: "bad",
  Error: "bad",
  Progressing: "info",
  Running: "info",
  Provision: "info",
  Install: "info",
  Pending: "warn",
  ContainerCreating: "warn",
  Aborted: "warn",
}

const LABELS: Record<string, string> = { True: "Ready", False: "Not ready", Unknown: "Unknown" }

export function toneOf(value?: string): BadgeVariant {
  return (value && TONES[value]) || "neutral"
}

export function labelOf(value?: string): string {
  return value ? (LABELS[value] ?? value) : "Unknown"
}

export function archetypeOf(archetype: string, groups?: string[]): string {
  if (archetype === "chart") return "A · Helm"
  return (groups?.length ?? 0) > 1 ? "C · multi-VM" : "B · VM"
}
