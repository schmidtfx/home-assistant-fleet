import type { ActionType, Severity, Task } from './api'

export const actionLabels: Record<ActionType, string> = {
  install_update: 'Install',
  skip_update: 'Skip',
  restart_core: 'Restart Core',
  reboot_host: 'Reboot host',
  create_backup: 'Create backup',
}

const otherActionLabels: Record<string, string> = {
  acknowledge: 'Acknowledge',
  unacknowledge: 'Unacknowledge',
  instance_create: 'Add instance',
  instance_update: 'Change instance',
  instance_delete: 'Remove instance',
  rollout_create: 'Start rollout',
  rollout_pause: 'Pause rollout',
  rollout_resume: 'Resume rollout',
  rollout_cancel: 'Cancel rollout',
  rollout_skip: 'Skip rollout step',
  settings_update: 'Change settings',
}

export const rolloutStateColor: Record<string, string> = {
  running: 'blue',
  paused: 'orange',
  completed: 'green',
  cancelled: 'gray',
}

export const stepStateColor: Record<string, string> = {
  pending: 'gray',
  waiting: 'gray',
  installing: 'blue',
  verifying: 'blue',
  done: 'green',
  failed: 'red',
  skipped: 'gray',
}

export const actionLabel = (a: string) => actionLabels[a as ActionType] ?? otherActionLabels[a] ?? a

/** Whether a task is currently hidden by an acknowledgement (snoozes end on their own). */
export function isAcked(t: Task): boolean {
  return !!t.ack && (!t.ack.expires_at || new Date(t.ack.expires_at).getTime() > Date.now())
}

export const kindLabels: Record<string, string> = {
  update: 'Update',
  repair: 'Repair',
  reboot_required: 'Reboot required',
  restart_required: 'Restart required',
  unhealthy: 'Unhealthy',
  unsupported: 'Unsupported',
  reauth: 'Re-auth',
  setup_failed: 'Setup failed',
  backup_stale: 'Backup',
  connectivity: 'Connectivity',
  disk_low: 'Disk space',
  cert_expiring: 'Certificate',
  entities_unavailable: 'Unavailable entities',
  battery_low: 'Batteries',
  log_errors: 'Log errors',
}

/** What each switchable check reports, for the settings forms. */
export const checkDescriptions: Record<string, string> = {
  repair: 'Issues listed under Settings → Repairs.',
  restart_required: 'Repairs that ask for a Home Assistant restart.',
  reauth: 'Integrations waiting for re-authentication.',
  setup_failed: 'Integrations that failed to set up.',
  backup_stale: 'No recent backup.',
  reboot_required: 'Supervisor asks for a host reboot.',
  unhealthy: 'Supervisor reports the system as unhealthy.',
  unsupported: 'Supervisor reports the system as unsupported.',
  disk_low: 'Low free disk space (Supervisor installs).',
  cert_expiring: 'TLS certificate expires soon.',
  entities_unavailable: 'Integrations with several entities unavailable for over an hour. Off: the entity registry is not fetched.',
  battery_low: 'Battery sensors below the threshold.',
  log_errors: 'Integrations logging repeated errors. Off: the system log is not fetched.',
}

/** Human-readable duration, e.g. "45 min", "3.5 h", "2.1 days". */
export function formatDuration(seconds: number): string {
  if (seconds < 60) return `${Math.round(seconds)} s`
  if (seconds < 3600) return `${Math.round(seconds / 60)} min`
  if (seconds < 86400) return `${(seconds / 3600).toFixed(1)} h`
  return `${(seconds / 86400).toFixed(1)} days`
}

export const severityColor: Record<Severity, string> = {
  critical: 'red',
  warning: 'orange',
  info: 'blue',
}

const platformUpdates = new Set([
  'update.home_assistant_core_update',
  'update.home_assistant_operating_system_update',
  'update.home_assistant_supervisor_update',
])

/** Whether running the action will interrupt the instance (restart/reboot). */
export function isDisruptive(task: Task, action: ActionType): boolean {
  if (action === 'restart_core' || action === 'reboot_host') return true
  return action === 'install_update' && platformUpdates.has(String(task.source?.entity_id ?? ''))
}

export function isPlatformUpdate(task: Task): boolean {
  return platformUpdates.has(String(task.source?.entity_id ?? ''))
}

export function relativeTime(iso?: string): string {
  if (!iso) return '—'
  const s = Math.round((Date.now() - new Date(iso).getTime()) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)} min ago`
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`
  return `${Math.floor(s / 86400)} d ago`
}
