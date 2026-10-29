import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { MantineProvider } from '@mantine/core'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import Rollouts from './Rollouts'
import type { Rollout } from '../api'

const paused: Rollout = {
  id: 'r1',
  created_by: 'alice',
  entity_id: 'update.mosquitto_broker_update',
  title: 'Update Mosquitto broker to 6.5.1',
  target_version: '6.5.1',
  backup: true,
  canary_count: 1,
  gate_minutes: 15,
  ai_gate: true,
  state: 'paused',
  message: 'Paused: Cabin: new critical task after the update: MQTT broken',
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
  steps: [
    { ord: 0, instance_id: 'a', canary: true, state: 'failed', action_id: 'act1', message: 'new critical task after the update: MQTT broken' },
    { ord: 1, instance_id: 'b', canary: false, state: 'pending' },
  ],
}

function wrap(role: 'viewer' | 'operator') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  qc.setQueryData(['me'], { username: 'u', role })
  qc.setQueryData(['instances'], [{ id: 'a', name: 'Cabin', tasks: [], ai_enabled: true }, { id: 'b', name: 'Office', tasks: [] }])
  qc.setQueryData(['ai-status'], { enabled: true })
  qc.setQueryData(['rollouts'], [paused])
  return render(
    <MantineProvider>
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <Rollouts />
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  )
}

describe('Rollouts', () => {
  it('shows why a rollout paused and offers resume and skip', () => {
    wrap('operator')
    expect(screen.getByText(/Paused: Cabin/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Resume' })).toBeInTheDocument()
    const canary = screen.getByRole('link', { name: 'Cabin' }).closest('tr')!
    expect(within(canary).getByText('canary')).toBeInTheDocument()
    expect(within(canary).getByText('failed')).toBeInTheDocument()
    expect(within(canary).getByRole('button', { name: 'Skip' })).toBeInTheDocument()
    expect(within(canary).getByRole('button', { name: 'AI check' })).toBeInTheDocument()
    expect(screen.getByText(/health check 15 min \+ AI/)).toBeInTheDocument()
  })

  it('is read-only for viewers', () => {
    wrap('viewer')
    expect(screen.queryByRole('button', { name: 'Resume' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Skip' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'AI check' })).not.toBeInTheDocument()
  })
})
