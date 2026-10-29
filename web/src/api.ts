// Typed client for the hafleet REST API.

export type Severity = 'info' | 'warning' | 'critical'

export type ActionType = 'install_update' | 'skip_update' | 'restart_core' | 'reboot_host' | 'create_backup'

export interface Task {
  id: string
  instance_id: string
  kind: string
  severity: Severity
  title: string
  detail?: string
  source?: Record<string, unknown>
  actions: ActionType[]
  first_seen: string
  ack?: TaskAck
}

export interface TaskAck {
  by: string
  note?: string
  at: string
  expires_at?: string
}

export interface InstanceStatus {
  state: 'disabled' | 'connecting' | 'connected' | 'error'
  error?: string
  ha_version?: string
  location_name?: string
  supervisor: boolean
  is_admin: boolean
  user_id?: string // HA user of the token; pick it in the hafleet integration
  warnings: string[]
  integrations: string[]
  last_sync?: string
}

export interface Instance {
  id: string
  name: string
  url: string
  enabled: boolean
  ai_enabled: boolean
  allow_insecure_http: boolean
  has_ca_pem: boolean
  cert_fingerprint: string
  maint_window: string // "HH:MM-HH:MM" server local time, '' = any time
  publish_status: boolean
  settings: CheckSettings // overrides of the fleet-wide settings
  status: InstanceStatus
  tasks: Task[]
}

export interface ActionRecord {
  id: string
  instance_id: string
  task_id: string
  action: ActionType
  target: string
  username: string
  status: 'running' | 'succeeded' | 'failed'
  message?: string
  started_at: string
  finished_at?: string
}

export interface AuditEntry {
  id: number
  time: string
  username: string
  instance_id: string
  instance_name: string
  action: string
  target: string
  result: string
  message?: string
}

export interface InstanceInput {
  name?: string
  url?: string
  token?: string
  ca_pem?: string
  cert_fingerprint?: string
  allow_insecure_http?: boolean
  enabled?: boolean
  ai_enabled?: boolean
  maint_window?: string
  publish_status?: boolean
  settings?: CheckSettings
}

/** Check switches and thresholds. Unset fields inherit (instance → fleet → command-line default). */
export interface CheckSettings {
  checks?: Record<string, boolean> // task kind → enabled
  disk_free_min?: number
  battery_min?: number
  backup_max_age_hours?: number
}

export interface SettingsResponse {
  settings: CheckSettings
  defaults: Required<Omit<CheckSettings, 'checks'>>
  switchable: string[]
}

export interface TestResult {
  ok: boolean
  error?: string
  ha_version?: string
  location_name?: string
  supervisor?: boolean
  is_admin?: boolean
}

export interface AIStatus {
  enabled: boolean
  provider?: string
  model?: string
}

export interface Review {
  instance_id: string
  task_id: string
  entity_id: string
  version: string
  risk: 'low' | 'review' | 'high'
  summary: string[]
  affected: { integration: string; change: string; action_needed: string }[]
  notes_complete: boolean
  notes_source: 'home_assistant' | 'release_url' | 'none'
  provider: string
  model: string
  created_at: string
}

export interface HealthCheck {
  instance_id: string
  action_id: string
  verdict: 'ok' | 'suspect' | 'broken'
  findings: string[]
  steps: string[]
  docs_url?: string
  provider: string
  model: string
  created_at: string
}

export interface Explanation {
  task_id: string
  likely_cause: string
  steps: string[]
  urgency: 'low' | 'medium' | 'high'
  docs_url?: string
  provider: string
  model: string
  created_at: string
}

export interface Briefing {
  headline: string
  urgent: { instance_id: string; task_id: string; why: string }[]
  suggested_order: { instance_id: string; task_ids: string[]; reason: string }[]
  notes: string
  instances: number
  provider: string
  model: string
  created_at: string
}

export interface Proposal {
  id: string
  instance_id: string
  instance_name: string
  task: Task
  action: ActionType
  backup: boolean
  reason: string
}

export interface ChatEvent {
  type: 'conversation' | 'text' | 'tool' | 'proposal' | 'done' | 'error'
  conversation_id?: string
  text?: string
  tool?: string
  status?: 'running' | 'ok' | 'error'
  proposal?: Proposal
}

export class ApiError extends Error {
  readonly status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

function csrfToken(): string {
  const c = document.cookie.split('; ').find((c) => c.startsWith('hafleet_csrf='))
  return c ? decodeURIComponent(c.slice('hafleet_csrf='.length)) : ''
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (method !== 'GET') headers['X-CSRF-Token'] = csrfToken()
  const res = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  })
  if (!res.ok) {
    let msg = res.statusText
    try {
      msg = (await res.json()).error ?? msg
    } catch {
      // non-JSON error body
    }
    throw new ApiError(res.status, msg)
  }
  const text = await res.text()
  return (text ? JSON.parse(text) : undefined) as T
}

export interface HistoryDay {
  date: string // YYYY-MM-DD, server local time
  opened: number
  closed: number
  open: number // still open at the end of the day (or now, for today)
  open_by_severity: Partial<Record<Severity, number>>
}

export interface KindStats {
  kind: string
  opened: number
  resolved: number
  mean_seconds: number
  median_seconds: number
}

export interface RecurringTask {
  instance_id: string
  task_id: string
  kind: string
  title: string
  count: number
  last_opened: string
}

export interface HistorySummary {
  since: string
  days: HistoryDay[]
  kinds: KindStats[]
  recurring: RecurringTask[]
}

export interface DriftInstance {
  id: string
  name: string
  known: boolean // false until the instance has synced once
}

export interface DriftRow {
  category: 'version' | 'addon' | 'integration'
  key: string
  label: string
  values: Record<string, string> // by instance id; missing = not installed
  majority: string // '' = most instances do not have it
  has_majority: boolean // false on a tie: no instance is the odd one out
  drift: boolean
}

export interface Drift {
  instances: DriftInstance[]
  rows: DriftRow[]
}

export type RolloutState = 'running' | 'paused' | 'completed' | 'cancelled'
export type StepState = 'pending' | 'waiting' | 'installing' | 'verifying' | 'done' | 'failed' | 'skipped'

export interface RolloutStep {
  ord: number
  instance_id: string
  canary: boolean
  state: StepState
  action_id?: string
  message?: string
  started_at?: string
  finished_at?: string
}

export interface Rollout {
  id: string
  created_by: string
  entity_id: string
  title: string
  target_version: string
  backup: boolean
  canary_count: number
  gate_minutes: number
  ai_gate: boolean
  start_at?: string
  state: RolloutState
  message?: string
  created_at: string
  updated_at: string
  steps: RolloutStep[]
}

export interface RolloutInput {
  entity_id: string
  target_version: string
  instances: string[] // in rollout order; the first canary_count are canaries
  canary_count: number
  backup: boolean
  gate_minutes: number
  ai_gate: boolean
  start_at?: string
}

/** viewer < operator < admin; each role includes the rights of the ones before it. */
export type Role = 'viewer' | 'operator' | 'admin'

export interface Me {
  username: string
  role: Role
}

export const api = {
  login: (username: string, password: string) =>
    request<Me>('POST', '/api/auth/login', { username, password }),
  logout: () => request<void>('POST', '/api/auth/logout'),
  me: () => request<Me>('GET', '/api/auth/me'),

  instances: () => request<Instance[]>('GET', '/api/instances'),
  createInstance: (body: InstanceInput) => request<Instance>('POST', '/api/instances', body),
  updateInstance: (id: string, body: InstanceInput) => request<Instance>('PATCH', `/api/instances/${id}`, body),
  deleteInstance: (id: string) => request<void>('DELETE', `/api/instances/${id}`),
  testInstance: (body: InstanceInput & { instance_id?: string }) =>
    request<TestResult>('POST', '/api/instances/test', body),
  resync: (id: string) => request<void>('POST', `/api/instances/${id}/resync`),

  submitAction: (instanceId: string, taskId: string, action: ActionType, backup = false) =>
    request<ActionRecord>('POST', `/api/instances/${instanceId}/actions`, { task_id: taskId, action, backup }),
  actions: () => request<ActionRecord[]>('GET', '/api/actions'),
  releaseNotes: (instanceId: string, taskId: string) =>
    request<{ notes: string | null; release_url?: string }>(
      'GET',
      `/api/instances/${instanceId}/tasks/${taskId}/release-notes`,
    ),
  audit: (limit = 200) => request<AuditEntry[]>('GET', `/api/audit?limit=${limit}`),
  drift: () => request<Drift>('GET', '/api/drift'),
  rollouts: () => request<Rollout[]>('GET', '/api/rollouts'),
  createRollout: (body: RolloutInput) => request<Rollout>('POST', '/api/rollouts', body),
  rolloutControl: (id: string, op: 'pause' | 'resume' | 'cancel') => request<Rollout>('POST', `/api/rollouts/${id}/${op}`),
  skipRolloutStep: (id: string, ord: number) => request<Rollout>('POST', `/api/rollouts/${id}/steps/${ord}/skip`),
  settings: () => request<SettingsResponse>('GET', '/api/settings'),
  saveSettings: (body: CheckSettings) => request<SettingsResponse>('PUT', '/api/settings', body),
  history: (days: number, instance?: string) =>
    request<HistorySummary>(
      'GET',
      `/api/history?days=${days}` + (instance ? `&instance=${encodeURIComponent(instance)}` : ''),
    ),

  ack: (instanceId: string, taskId: string, note: string, durationHours: number) =>
    request<void>('POST', `/api/instances/${instanceId}/tasks/${taskId}/ack`, { note, duration_hours: durationHours }),
  unack: (instanceId: string, taskId: string) =>
    request<void>('DELETE', `/api/instances/${instanceId}/tasks/${taskId}/ack`),

  aiStatus: () => request<AIStatus>('GET', '/api/ai/status'),
  aiReviews: () => request<Record<string, Review>>('GET', '/api/ai/reviews'),
  aiReview: (instanceId: string, taskId: string, refresh = false) =>
    request<Review>('POST', `/api/instances/${instanceId}/tasks/${taskId}/ai/review`, { refresh }),
  aiHealth: (instanceId: string, actionId: string, refresh = false) =>
    request<HealthCheck>('POST', `/api/instances/${instanceId}/actions/${actionId}/ai/health`, { refresh }),
  aiExplain: (instanceId: string, taskId: string, refresh = false) =>
    request<Explanation>('POST', `/api/instances/${instanceId}/tasks/${taskId}/ai/explain`, { refresh }),
  briefing: () => request<{ briefing: Briefing | null }>('GET', '/api/ai/briefing'),
  newBriefing: () => request<{ briefing: Briefing }>('POST', '/api/ai/briefing'),
  chat: (conversationId: string | undefined, message: string, onEvent: (ev: ChatEvent) => void, signal?: AbortSignal) =>
    chatStream(conversationId, message, onEvent, signal),
}

/** Parses complete Server-Sent Event frames from buf; returns events and the unparsed remainder. */
export function parseSSE(buf: string): { events: { event: string; data: string }[]; rest: string } {
  const events: { event: string; data: string }[] = []
  let idx: number
  while ((idx = buf.indexOf('\n\n')) >= 0) {
    const frame = buf.slice(0, idx)
    buf = buf.slice(idx + 2)
    let event = 'message'
    const data: string[] = []
    for (const line of frame.split('\n')) {
      if (line.startsWith('event:')) event = line.slice(6).trim()
      else if (line.startsWith('data:')) data.push(line.slice(5).trimStart())
    }
    if (data.length) events.push({ event, data: data.join('\n') })
  }
  return { events, rest: buf }
}

// EventSource cannot POST, so the chat stream is read from fetch directly.
async function chatStream(
  conversationId: string | undefined,
  message: string,
  onEvent: (ev: ChatEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch('/api/ai/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken() },
    body: JSON.stringify({ conversation_id: conversationId ?? '', message }),
    credentials: 'same-origin',
    signal,
  })
  if (!res.ok || !res.body) {
    let msg = res.statusText
    try {
      msg = (await res.json()).error ?? msg
    } catch {
      // non-JSON error body
    }
    throw new ApiError(res.status, msg)
  }
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  for (;;) {
    const { value, done } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    const parsed = parseSSE(buf)
    buf = parsed.rest
    for (const e of parsed.events) onEvent(JSON.parse(e.data) as ChatEvent)
  }
}
