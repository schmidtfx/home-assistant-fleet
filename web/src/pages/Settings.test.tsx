import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MantineProvider } from '@mantine/core'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import Settings from './Settings'
import { api, type SettingsResponse } from '../api'

const data: SettingsResponse = {
  settings: { checks: { log_errors: false } },
  defaults: { disk_free_min: 15, battery_min: 15, backup_max_age_hours: 168 },
  switchable: ['repair', 'entities_unavailable', 'battery_low', 'log_errors'],
}

function wrap(role: 'operator' | 'admin') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  qc.setQueryData(['me'], { username: 'u', role })
  qc.setQueryData(['settings'], data)
  return render(
    <MantineProvider>
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <Settings />
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  )
}

// A switch's accessible name also contains its description.
const toggle = (label: string) => screen.getByRole('switch', { name: new RegExp(`^${label}`) })

afterEach(() => vi.restoreAllMocks())

describe('Settings', () => {
  it('shows the current switches and is read-only for non-admins', () => {
    wrap('operator')
    expect(toggle('Unavailable entities')).toBeChecked()
    expect(toggle('Log errors')).not.toBeChecked()
    expect(toggle('Unavailable entities')).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
  })

  it('saves switched-off checks and thresholds', async () => {
    const user = userEvent.setup()
    const save = vi.spyOn(api, 'saveSettings').mockResolvedValue(data)
    wrap('admin')
    await user.click(toggle('Unavailable entities'))
    await user.click(toggle('Log errors'))
    await user.type(screen.getByLabelText('Low battery below'), '25')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(save).toHaveBeenCalledWith({ checks: { entities_unavailable: false }, battery_min: 25 })
  })
})
