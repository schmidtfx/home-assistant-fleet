import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MantineProvider } from '@mantine/core'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import type { ReactNode } from 'react'
import TaskTable from './TaskTable'
import type { FleetTask } from '../hooks'

const coreUpdate: FleetTask = {
  id: 't1',
  instance_id: 'i1',
  instance_name: 'Cabin',
  kind: 'update',
  severity: 'warning',
  title: 'Update Home Assistant Core to 2026.10.2',
  detail: '2026.10.1 → 2026.10.2',
  source: { entity_id: 'update.home_assistant_core_update', supports_backup: true },
  actions: ['install_update', 'skip_update'],
  first_seen: new Date().toISOString(),
}

const repair: FleetTask = {
  id: 't2',
  instance_id: 'i1',
  instance_name: 'Cabin',
  kind: 'repair',
  severity: 'critical',
  title: 'Deprecated YAML',
  actions: [],
  first_seen: new Date().toISOString(),
}

function wrap(ui: ReactNode, seed: Record<string, unknown> = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  qc.setQueryData(['actions'], [])
  qc.setQueryData(['me'], { username: 'admin', role: 'admin' })
  qc.setQueryData(['instances'], [])
  qc.setQueryData(['ai-status'], { enabled: false })
  for (const [k, v] of Object.entries(seed)) qc.setQueryData([k], v)
  return render(
    <MantineProvider>
      <QueryClientProvider client={qc}>
        <MemoryRouter>{ui}</MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  )
}

describe('TaskTable', () => {
  const fetchMock = vi.fn()
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
    document.cookie = 'hafleet_csrf=csrf-123'
  })
  afterEach(() => vi.unstubAllGlobals())

  it('shows an empty state', () => {
    wrap(<TaskTable tasks={[]} />)
    expect(screen.getByText(/Nothing to do/)).toBeInTheDocument()
  })

  it('renders tasks with instance, severity and actions', () => {
    wrap(<TaskTable tasks={[coreUpdate, repair]} />)
    const rows = screen.getAllByRole('row')
    expect(rows).toHaveLength(3)
    expect(within(rows[1]).getByText('Cabin')).toBeInTheDocument()
    expect(within(rows[1]).getByRole('button', { name: 'Install' })).toBeInTheDocument()
    expect(within(rows[2]).getByText('critical')).toBeInTheDocument()
    expect(within(rows[2]).queryByRole('button', { name: 'Install' })).not.toBeInTheDocument()
  })

  it('lists the entities of a health task on demand', async () => {
    const user = userEvent.setup()
    const battery: FleetTask = {
      id: 't3', instance_id: 'i1', instance_name: 'Cabin', kind: 'battery_low', severity: 'info',
      title: '2–5 low batteries', detail: '2 battery sensors are below 15%.', actions: [],
      source: { count: 2, entities: ['sensor.door_battery', 'sensor.window_battery'] },
      first_seen: new Date().toISOString(),
    }
    wrap(<TaskTable tasks={[battery]} />)
    expect(screen.getByText('Batteries')).toBeInTheDocument()
    expect(screen.queryByText('sensor.door_battery')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Show 2 entities' }))
    expect(screen.getByText('sensor.door_battery')).toBeInTheDocument()
  })

  it('hides action and acknowledge buttons from viewers', () => {
    wrap(<TaskTable tasks={[coreUpdate]} />, { me: { username: 'v', role: 'viewer' } })
    const row = screen.getAllByRole('row')[1]
    expect(within(row).getByText('Cabin')).toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: 'Install' })).not.toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: 'Skip' })).not.toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: 'Acknowledge' })).not.toBeInTheDocument()
  })

  it('requires confirmation and submits the action with backup and CSRF token', async () => {
    const user = userEvent.setup()
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ id: 'a1', status: 'running' }), {
        status: 202,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    wrap(<TaskTable tasks={[coreUpdate]} />)

    await user.click(screen.getByRole('button', { name: 'Install' }))
    expect(fetchMock).not.toHaveBeenCalled()

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/restart or reboot Cabin/)).toBeInTheDocument()
    expect(within(dialog).getByLabelText('Create a backup before updating')).toBeChecked()

    await user.click(within(dialog).getByRole('button', { name: 'Install' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/instances/i1/actions')
    expect(init.method).toBe('POST')
    expect(init.headers['X-CSRF-Token']).toBe('csrf-123')
    expect(JSON.parse(init.body)).toEqual({ task_id: 't1', action: 'install_update', backup: true })
  })

  it('cancelling the dialog does not submit', async () => {
    const user = userEvent.setup()
    wrap(<TaskTable tasks={[coreUpdate]} />)
    await user.click(screen.getByRole('button', { name: 'Skip' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
