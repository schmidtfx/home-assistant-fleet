import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MantineProvider } from '@mantine/core'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import Drift from './Drift'
import type { Drift as DriftData } from '../api'

const data: DriftData = {
  instances: [
    { id: 'a', name: 'Cabin', known: true },
    { id: 'b', name: 'Office', known: true },
    { id: 'c', name: 'Garage', known: false },
  ],
  rows: [
    // As if a third known instance also ran 2026.9.3 (not shown): a clear majority.
    { category: 'version', key: 'core', label: 'Home Assistant', values: { a: '2026.10.1', b: '2026.9.3' }, majority: '2026.9.3', has_majority: true, drift: true },
    { category: 'addon', key: 'core_samba', label: 'Samba share', values: { a: '12.3.2', b: '12.3.2' }, majority: '12.3.2', has_majority: true, drift: false },
    { category: 'addon', key: 'core_mosquitto', label: 'Mosquitto', values: { a: '6.4', b: '6.5' }, majority: '', has_majority: false, drift: true },
    { category: 'integration', key: 'hue', label: 'hue', values: { a: 'installed' }, majority: 'installed', has_majority: true, drift: true },
  ],
}

function wrap() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  qc.setQueryData(['drift'], data)
  return render(
    <MantineProvider>
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <Drift />
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  )
}

const row = (label: string) => screen.getByText(label).closest('tr')!

describe('Drift', () => {
  it('shows only differing items by default and marks the odd ones out', () => {
    wrap()
    expect(screen.queryByText('Samba share')).not.toBeInTheDocument()
    const core = within(row('Home Assistant')).getAllByRole('cell')
    expect(core[1]).toHaveTextContent('2026.10.1') // differs from the majority: badge
    expect(within(core[1]).getByText('2026.10.1').closest('.mantine-Badge-root')).not.toBeNull()
    expect(within(core[2]).getByText('2026.9.3').closest('.mantine-Badge-root')).toBeNull()
    expect(core[3]).toHaveTextContent('?') // never synced
    expect(within(row('hue')).getByText('missing')).toBeInTheDocument()
  })

  it('marks the row, not a cell, when there is no majority', () => {
    wrap()
    const r = row('Mosquitto')
    expect(within(r).getByText('differs')).toBeInTheDocument()
    expect(within(r).getByText('6.4').closest('.mantine-Badge-root')).toBeNull()
    expect(within(r).getByText('6.5').closest('.mantine-Badge-root')).toBeNull()
  })

  it('shows matching items when the filter is off', async () => {
    const user = userEvent.setup()
    wrap()
    await user.click(screen.getByLabelText('Only differences'))
    expect(screen.getByText('Samba share')).toBeInTheDocument()
  })
})
