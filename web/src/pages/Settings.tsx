import { useState } from 'react'
import { Alert, Button, Group, Loader, Paper, Stack, Switch, Text, Title } from '@mantine/core'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api, type CheckSettings, type SettingsResponse } from '../api'
import { CheckGroups, ThresholdFields } from '../components/CheckSettingsFields'
import { useCan, useSettings } from '../hooks'
import { checkDescriptions, kindLabels } from '../labels'

export default function Settings() {
  const { data, isPending, isError, error } = useSettings()
  if (isPending) return <Loader />
  if (isError) return <Alert color="red">{error.message}</Alert>
  return <SettingsForm data={data} />
}

function SettingsForm({ data }: { data: SettingsResponse }) {
  const canEdit = useCan('admin')
  const qc = useQueryClient()
  const [value, setValue] = useState<CheckSettings>(data.settings)
  const save = useMutation({
    mutationFn: () => api.saveSettings(value),
    onSuccess: (res) => {
      qc.setQueryData(['settings'], res)
      qc.invalidateQueries({ queryKey: ['instances'] })
    },
  })
  const enabled = (kind: string) => value.checks?.[kind] ?? true
  const setEnabled = (kind: string, on: boolean) => {
    // Only store switched-off checks; on is the default.
    const checks = { ...value.checks }
    if (on) delete checks[kind]
    else checks[kind] = false
    setValue({ ...value, checks })
  }

  return (
    <Stack maw={720}>
      <Title order={2}>Settings</Title>
      <Text c="dimmed" size="sm">
        These apply to every instance. An instance can override them in its settings. Switched-off checks create no
        tasks, and data that only they need is not fetched from Home Assistant.
      </Text>
      {!canEdit && <Alert color="gray">Only admins can change settings.</Alert>}

      <Paper withBorder p="md">
        <Title order={4} mb="sm">
          Checks
        </Title>
        <CheckGroups
          switchable={data.switchable}
          row={(kind) => (
            <Switch
              key={kind}
              label={kindLabels[kind] ?? kind}
              description={checkDescriptions[kind]}
              checked={enabled(kind)}
              disabled={!canEdit}
              onChange={(e) => setEnabled(kind, e.currentTarget.checked)}
            />
          )}
        />
        <Text size="sm" c="dimmed" mt="md">
          Updates and connectivity are always checked.
        </Text>
      </Paper>

      <Paper withBorder p="md">
        <Title order={4} mb="sm">
          Thresholds
        </Title>
        <ThresholdFields value={value} inherited={data.defaults} onChange={setValue} disabled={!canEdit} />
      </Paper>

      {save.isError && <Alert color="red">{save.error.message}</Alert>}
      {save.isSuccess && <Alert color="green">Saved. Instances are resyncing.</Alert>}
      {canEdit && (
        <Group justify="flex-end">
          <Button onClick={() => save.mutate()} loading={save.isPending}>
            Save
          </Button>
        </Group>
      )}
    </Stack>
  )
}
