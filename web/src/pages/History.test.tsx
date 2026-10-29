import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MantineProvider } from '@mantine/core'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import History from './History'
import type { HistorySummary } from '../api'

const summary: HistorySummary = {
  since: '2026-10-28T00:00:00Z',
  days: [
    { date: '2026-10-28', opened: 3, closed: 1, open: 2, open_by_severity: { warning: 2 } },
    { date: '2026-10-29', opened: 1, closed: 2, open: 1, open_by_severity: { critical: 1 } },
  ],
  kinds: [
    { kind: 'setup_failed', opened: 3, resolved: 2, mean_seconds: 7200, median_seconds: 5400 },
    { kind: 'repair', opened: 1, resolved: 1, mean_seconds: 600, median_seconds: 600 },
  ],
  recurring: [
    { instance_id: 'i1', task_id: 't1', kind: 'setup_failed', title: 'Setup failed: MQTT', count: 3, last_opened: new Date().toISOString() },
  ],
}

function wrap() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  qc.setQueryData(['instances'], [{ id: 'i1', name: 'Cabin', tasks: [] }])
  qc.setQueryData(['history', '30', null], summary)
  return render(
    <MantineProvider>
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <History />
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  )
}

describe('History', () => {
  it('shows totals, time to resolve and recurring tasks', () => {
    wrap()
    const stat = (label: string) => screen.getByRole('group', { name: label })
    expect(within(stat('Open now')).getByText('1')).toBeInTheDocument()
    expect(within(stat('Opened')).getByText('4')).toBeInTheDocument()
    expect(within(stat('Resolved')).getByText('3')).toBeInTheDocument()
    // (2×7200 + 600) / 3 = 5000 s
    expect(within(stat('Mean time to resolve')).getByText('1.4 h')).toBeInTheDocument()
    expect(screen.getByText('Setup failed: MQTT')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Cabin' })).toHaveAttribute('href', '/instances/i1')
    expect(screen.getByText('1.5 h')).toBeInTheDocument() // median for setup_failed
  })

  it('offers the daily numbers as a table', async () => {
    const user = userEvent.setup()
    wrap()
    await user.click(screen.getByText('Table'))
    const rows = screen.getAllByRole('row').filter((r) => within(r).queryByText(/Oct 2[89]/))
    expect(rows).toHaveLength(2)
    expect(within(rows[1]).getAllByRole('cell').map((c) => c.textContent)).toEqual(['Oct 29', '1', '0', '0', '1', '2'])
  })
})
