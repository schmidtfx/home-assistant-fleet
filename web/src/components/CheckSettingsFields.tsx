import type { ReactNode } from 'react'
import { Group, Loader, NumberInput, SegmentedControl, Stack, Text } from '@mantine/core'
import type { CheckSettings } from '../api'
import { useSettings } from '../hooks'
import { checkDescriptions, kindLabels } from '../labels'

const groups: { title: string; kinds: string[] }[] = [
  { title: 'Home Assistant', kinds: ['repair', 'restart_required', 'reauth', 'setup_failed', 'backup_stale'] },
  { title: 'Supervisor', kinds: ['reboot_required', 'unhealthy', 'unsupported'] },
  { title: 'Health', kinds: ['disk_low', 'cert_expiring', 'entities_unavailable', 'battery_low', 'log_errors'] },
]

/** Lists the switchable kinds by group, rendering each with row(kind). */
export function CheckGroups({ switchable, row }: { switchable: string[]; row: (kind: string) => ReactNode }) {
  const known = groups.flatMap((g) => g.kinds)
  const all = [...groups, { title: 'Other', kinds: switchable.filter((k) => !known.includes(k)) }]
  return (
    <Stack>
      {all
        .map((g) => ({ ...g, kinds: g.kinds.filter((k) => switchable.includes(k)) }))
        .filter((g) => g.kinds.length > 0)
        .map((g) => (
          <Stack key={g.title} gap="xs">
            <Text fw={500} size="sm">
              {g.title}
            </Text>
            {g.kinds.map(row)}
          </Stack>
        ))}
    </Stack>
  )
}

type Threshold = 'disk_free_min' | 'battery_min' | 'backup_max_age_hours'

const thresholds: { key: Threshold; label: string; suffix: string; max: number }[] = [
  { key: 'disk_free_min', label: 'Low disk space below', suffix: ' %', max: 99 },
  { key: 'battery_min', label: 'Low battery below', suffix: ' %', max: 99 },
  { key: 'backup_max_age_hours', label: 'Backup is stale after', suffix: ' h', max: 8760 },
]

/**
 * Inputs for the thresholds. An empty input means "inherit", shown with the
 * inherited value as placeholder.
 */
export function ThresholdFields({
  value,
  inherited,
  onChange,
  disabled,
}: {
  value: CheckSettings
  inherited: Required<Omit<CheckSettings, 'checks'>>
  onChange: (v: CheckSettings) => void
  disabled?: boolean
}) {
  return (
    <Group grow align="flex-start">
      {thresholds.map((t) => (
        <NumberInput
          key={t.key}
          label={t.label}
          description={`Empty: ${inherited[t.key]}${t.suffix}`}
          placeholder={String(inherited[t.key])}
          suffix={t.suffix}
          min={1}
          max={t.max}
          allowNegative={false}
          disabled={disabled}
          value={value[t.key] ?? ''}
          onChange={(v) => onChange({ ...value, [t.key]: typeof v === 'number' ? v : undefined })}
        />
      ))}
    </Group>
  )
}

/** Per-instance overrides: each check is Default (fleet setting), On or Off. */
export function CheckOverrides({ value, onChange }: { value: CheckSettings; onChange: (v: CheckSettings) => void }) {
  const { data, isError, error } = useSettings()
  if (isError) return <Text c="red" size="sm">{error.message}</Text>
  if (!data) return <Loader size="sm" />
  const fleet = data.settings
  const inherited = {
    disk_free_min: fleet.disk_free_min ?? data.defaults.disk_free_min,
    battery_min: fleet.battery_min ?? data.defaults.battery_min,
    backup_max_age_hours: fleet.backup_max_age_hours ?? data.defaults.backup_max_age_hours,
  }
  const setCheck = (kind: string, v: string) => {
    const checks = { ...value.checks }
    if (v === 'default') delete checks[kind]
    else checks[kind] = v === 'on'
    onChange({ ...value, checks })
  }
  return (
    <Stack>
      <Text size="sm" c="dimmed">
        Default uses the fleet-wide setting from the Settings page.
      </Text>
      <CheckGroups
        switchable={data.switchable}
        row={(kind) => {
          const own = value.checks?.[kind]
          return (
            <Group key={kind} justify="space-between" wrap="nowrap">
              <div>
                <Text size="sm">{kindLabels[kind] ?? kind}</Text>
                <Text size="xs" c="dimmed">
                  {checkDescriptions[kind]}
                </Text>
              </div>
              <SegmentedControl
                size="xs"
                aria-label={kindLabels[kind] ?? kind}
                value={own === undefined ? 'default' : own ? 'on' : 'off'}
                onChange={(v) => setCheck(kind, v)}
                data={[
                  { value: 'default', label: `Default (${(fleet.checks?.[kind] ?? true) ? 'on' : 'off'})` },
                  { value: 'on', label: 'On' },
                  { value: 'off', label: 'Off' },
                ]}
              />
            </Group>
          )
        }}
      />
      <ThresholdFields value={value} inherited={inherited} onChange={onChange} />
    </Stack>
  )
}
