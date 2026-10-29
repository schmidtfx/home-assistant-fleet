import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MantineProvider } from '@mantine/core'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import TaskTable from './TaskTable'
import type { FleetTask } from '../hooks'

const printer: FleetTask = {
  id: 't9',
  instance_id: 'i1',
  instance_name: 'Oberweningen',
  kind: 'setup_failed',
  severity: 'warning',
  title: 'Integration Canon MF645C (ipp) failed: Setup retry',
  actions: [],
  first_seen: new Date().toISOString(),
}

function wrap(tasks: FleetTask[]) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  qc.setQueryData(['actions'], [])
  qc.setQueryData(['me'], { username: 'admin', role: 'admin' })
  qc.setQueryData(['instances'], [])
  qc.setQueryData(['ai-status'], { enabled: false })
  return render(
    <MantineProvider>
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <TaskTable tasks={tasks} />
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  )
}

describe('acknowledging tasks', () => {
  const fetchMock = vi.fn()
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
    document.cookie = 'hafleet_csrf=c'
  })
  afterEach(() => vi.unstubAllGlobals())

  it('sends note and snooze duration', async () => {
    const user = userEvent.setup()
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))
    wrap([printer])
    await user.click(screen.getByRole('button', { name: 'Acknowledge' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByText('7 days'))
    await user.type(within(dialog).getByLabelText(/Note/), 'printer is off')
    await user.click(within(dialog).getByRole('button', { name: 'Acknowledge' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/instances/i1/tasks/t9/ack')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body)).toEqual({ note: 'printer is off', duration_hours: 168 })
  })

  it('shows acknowledged tasks dimmed with an unacknowledge button', async () => {
    const user = userEvent.setup()
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))
    wrap([{ ...printer, ack: { by: 'admin', note: 'printer is off', at: new Date().toISOString() } }])
    expect(screen.getByText('acknowledged: printer is off')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Unacknowledge' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    expect(fetchMock.mock.calls[0][1].method).toBe('DELETE')
  })

  it('treats expired snoozes as not acknowledged', () => {
    wrap([{ ...printer, ack: { by: 'admin', at: '2026-01-01T00:00:00Z', expires_at: '2026-01-02T00:00:00Z' } }])
    expect(screen.queryByText(/acknowledged/)).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Acknowledge' })).toBeInTheDocument()
  })
})
