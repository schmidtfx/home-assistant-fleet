import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MantineProvider } from '@mantine/core'
import { Notifications } from '@mantine/notifications'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import RolloutModal, { rolloutCandidates } from './RolloutModal'
import type { Instance, Task } from '../api'

const upd = (instance_id: string, id: string, latest: string, extra: Partial<Task> = {}): Task => ({
  id,
  instance_id,
  kind: 'update',
  severity: 'info',
  title: `Update Mosquitto broker to ${latest}`,
  detail: `6.4.0 → ${latest}`,
  source: { entity_id: 'update.mosquitto_broker_update', latest_version: latest, supports_backup: true },
  actions: ['install_update', 'skip_update'],
  first_seen: new Date().toISOString(),
  ...extra,
})

const inst = (id: string, name: string, tasks: Task[], maint_window = ''): Instance =>
  ({ id, name, tasks, maint_window, ai_enabled: false, status: { state: 'connected' } }) as unknown as Instance

const instances = [
  inst('a', 'Cabin', [upd('a', 'ta', '6.5.1')]),
  inst('b', 'Office', [upd('b', 'tb', '6.5.1')], '02:00-05:00'),
  inst('c', 'Garage', [upd('c', 'tc', '6.6.0')]), // other version
  inst('d', 'Shed', [upd('d', 'td', '6.5.1', { actions: ['skip_update'] })]), // cannot install
]

describe('rolloutCandidates', () => {
  it('matches entity and version and requires the install action', () => {
    expect(rolloutCandidates(instances, instances[0].tasks[0]).map((c) => c.instance.name)).toEqual(['Cabin', 'Office'])
  })
})

describe('RolloutModal', () => {
  const fetchMock = vi.fn()
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
    document.cookie = 'hafleet_csrf=csrf-123'
  })
  afterEach(() => vi.unstubAllGlobals())

  it('submits the chosen order, canaries and options', async () => {
    const user = userEvent.setup()
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ id: 'r1', steps: [] }), { status: 201 }))
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
    qc.setQueryData(['instances'], instances)
    qc.setQueryData(['ai-status'], { enabled: false })
    render(
      <MantineProvider>
        <Notifications />
        <QueryClientProvider client={qc}>
          <MemoryRouter>
            <RolloutModal task={instances[0].tasks[0]} opened onClose={() => {}} />
          </MemoryRouter>
        </QueryClientProvider>
      </MantineProvider>,
    )
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/window 02:00-05:00/)).toBeInTheDocument()
    // Office first: move it up. The canary badge follows the order.
    await user.click(within(dialog).getAllByRole('button', { name: 'Move up' })[1])
    const names = within(dialog).getAllByText(/^(Cabin|Office)$/).map((n) => n.textContent)
    expect(names).toEqual(['Office', 'Cabin'])
    await user.click(within(dialog).getByRole('button', { name: 'Start rollout on 2 instances' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/rollouts')
    expect(JSON.parse(init.body)).toEqual({
      entity_id: 'update.mosquitto_broker_update',
      target_version: '6.5.1',
      instances: ['b', 'a'],
      canary_count: 1,
      backup: true,
      gate_minutes: 15,
      ai_gate: false,
    })
  })
})
