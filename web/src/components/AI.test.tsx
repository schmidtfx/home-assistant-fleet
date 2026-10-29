import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MantineProvider } from '@mantine/core'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import type { ReactNode } from 'react'
import { parseSSE, type Review } from '../api'
import TaskTable from './TaskTable'
import Assistant from '../pages/Assistant'
import type { FleetTask } from '../hooks'

const update: FleetTask = {
  id: 't1',
  instance_id: 'i1',
  instance_name: 'Cabin',
  ai_enabled: true,
  kind: 'update',
  severity: 'warning',
  title: 'Update Home Assistant Core to 2026.11.0',
  source: { entity_id: 'update.home_assistant_core_update', supports_backup: true },
  actions: ['install_update'],
  first_seen: new Date().toISOString(),
}

const review: Review = {
  instance_id: 'i1',
  task_id: 't1',
  entity_id: 'update.home_assistant_core_update',
  version: '2026.11.0',
  risk: 'high',
  summary: ['MQTT state_topic is removed.'],
  affected: [{ integration: 'mqtt', change: 'state_topic removed', action_needed: 'Migrate to the new schema' }],
  notes_complete: true,
  notes_source: 'home_assistant',
  provider: 'fake',
  model: 'fake-model',
  created_at: new Date().toISOString(),
}

function wrap(ui: ReactNode, seed: Record<string, unknown>) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  qc.setQueryData(['actions'], [])
  qc.setQueryData(['me'], { username: 'admin', role: 'admin' })
  qc.setQueryData(['instances'], [])
  qc.setQueryData(['ai-status'], { enabled: true, provider: 'anthropic', model: 'claude-opus-5-5' })
  for (const [k, v] of Object.entries(seed)) qc.setQueryData([k], v)
  return render(
    <MantineProvider>
      <QueryClientProvider client={qc}>
        <MemoryRouter>{ui}</MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  )
}

describe('parseSSE', () => {
  it('parses complete frames and keeps the remainder', () => {
    const { events, rest } = parseSSE('event: text\ndata: {"a":1}\n\nevent: done\ndata: {}\n\nevent: par')
    expect(events).toEqual([
      { event: 'text', data: '{"a":1}' },
      { event: 'done', data: '{}' },
    ])
    expect(rest).toBe('event: par')
  })
})

describe('AI in the task table', () => {
  const fetchMock = vi.fn()
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
  })
  afterEach(() => vi.unstubAllGlobals())

  it('shows the cached risk badge and the review in the confirmation dialog', async () => {
    const user = userEvent.setup()
    wrap(<TaskTable tasks={[update]} />, { 'ai-reviews': { t1: review } })
    expect(screen.getByText('high risk')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Install' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('AI review: high risk')).toBeInTheDocument()
    expect(within(dialog).getByText(/Migrate to the new schema/)).toBeInTheDocument()
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('hides AI for instances without AI enabled', () => {
    wrap(<TaskTable tasks={[{ ...update, ai_enabled: false }]} />, { 'ai-reviews': { t1: review } })
    expect(screen.queryByText('high risk')).not.toBeInTheDocument()
  })
})

describe('Assistant', () => {
  const fetchMock = vi.fn()
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
    document.cookie = 'hafleet_csrf=csrf-1'
  })
  afterEach(() => vi.unstubAllGlobals())

  function sseResponse(events: object[]) {
    const body = events.map((e) => `event: ${(e as { type: string }).type}\ndata: ${JSON.stringify(e)}\n\n`).join('')
    return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } })
  }

  it('streams the reply and requires confirmation for proposals', async () => {
    const user = userEvent.setup()
    fetchMock.mockResolvedValueOnce(
      sseResponse([
        { type: 'conversation', conversation_id: 'c1' },
        { type: 'tool', tool: 'list_tasks', status: 'running' },
        { type: 'tool', tool: 'list_tasks', status: 'ok' },
        { type: 'text', text: 'Core has a **breaking** change.' },
        { type: 'proposal', proposal: { id: 'p1', instance_id: 'i1', instance_name: 'Cabin', task: update, action: 'install_update', backup: true, reason: 'New release' } },
        { type: 'done', conversation_id: 'c1' },
      ]),
    )
    wrap(<Assistant />, { 'ai-reviews': {} })
    await user.type(screen.getByPlaceholderText('Ask about your fleet…'), 'update cabin?')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    expect(await screen.findByText(/breaking/)).toBeInTheDocument()
    expect(screen.getByText('Looking up tasks')).toBeInTheDocument()
    expect(screen.getByText(/Proposed:/)).toBeInTheDocument()

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/ai/chat')
    expect(init.headers['X-CSRF-Token']).toBe('csrf-1')
    expect(JSON.parse(init.body)).toEqual({ conversation_id: '', message: 'update cabin?' })

    // Nothing is submitted until the user confirms in the dialog.
    await user.click(screen.getByRole('button', { name: 'Install' }))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ id: 'a1', status: 'running' }), { status: 202 }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Install' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    expect(fetchMock.mock.calls[1][0]).toBe('/api/instances/i1/actions')
  })
})
