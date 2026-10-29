import { useState } from 'react'
import { Alert, Anchor, Button, Group, List, Loader, Modal, Paper, Stack, Switch, Text, Title } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconExternalLink, IconRefresh } from '@tabler/icons-react'
import { useMutation } from '@tanstack/react-query'
import { useParams } from 'react-router-dom'
import { api } from '../api'
import StatusBadge from '../components/StatusBadge'
import TaskTable from '../components/TaskTable'
import { fleetTasks, runningAction, useActions, useCan, useInstances } from '../hooks'
import { isAcked, isPlatformUpdate, relativeTime } from '../labels'

export default function InstanceDetail() {
  const { id } = useParams()
  const { data: instances, isPending } = useInstances()
  const [bulkOpen, setBulkOpen] = useState(false)
  const inst = instances?.find((i) => i.id === id)

  const { data: actions } = useActions()
  const canOperate = useCan('operator')
  const resync = useMutation({ mutationFn: () => api.resync(id!) })
  const [showAcked, setShowAcked] = useState(false)
  const ackedCount = inst?.tasks.filter(isAcked).length ?? 0
  // Bulk install skips Core/OS/Supervisor updates (they restart the instance and should be run one at a time),
  // acknowledged updates, and updates that are already installing (in HA or via an action from hafleet).
  const bulkTasks =
    inst?.tasks.filter(
      (t) =>
        t.actions.includes('install_update') &&
        !isPlatformUpdate(t) &&
        !isAcked(t) &&
        t.source?.in_progress !== true &&
        !runningAction(actions, t.id),
    ) ?? []
  const bulk = useMutation({
    mutationFn: async () => {
      const results = await Promise.allSettled(
        bulkTasks.map((t) => api.submitAction(t.instance_id, t.id, 'install_update', t.source?.supports_backup === true)),
      )
      return results.filter((r) => r.status === 'rejected').length
    },
    onSuccess: (failed) => {
      setBulkOpen(false)
      notifications.show({
        color: failed ? 'orange' : 'blue',
        message: failed
          ? `${bulkTasks.length - failed} updates started, ${failed} could not be started`
          : `${bulkTasks.length} updates started`,
      })
    },
  })

  if (isPending) return <Loader />
  if (!inst) return <Alert color="red">Instance not found.</Alert>

  return (
    <Stack>
      <Group justify="space-between">
        <div>
          <Group gap="sm">
            <Title order={2}>{inst.name}</Title>
            <StatusBadge status={inst.status} />
          </Group>
          <Text size="sm" c="dimmed">
            <Anchor href={inst.url} target="_blank" rel="noopener noreferrer" size="sm">
              {inst.url} <IconExternalLink size={12} />
            </Anchor>
            {inst.status.ha_version && ` · Home Assistant ${inst.status.ha_version}`}
            {inst.status.supervisor && ' · Supervisor'}
            {inst.status.last_sync && ` · synced ${relativeTime(inst.status.last_sync)}`}
          </Text>
        </div>
        <Group display={canOperate ? undefined : 'none'}>
          {bulkTasks.length > 0 && (
            <Button variant="light" onClick={() => setBulkOpen(true)}>
              Install {bulkTasks.length} update{bulkTasks.length > 1 ? 's' : ''}
            </Button>
          )}
          <Button
            variant="default"
            leftSection={<IconRefresh size={16} />}
            onClick={() => resync.mutate()}
            loading={resync.isPending}
            disabled={!inst.enabled}
          >
            Refresh
          </Button>
        </Group>
      </Group>

      {inst.status.error && (
        <Alert color="red" title="Connection error">
          {inst.status.error}
        </Alert>
      )}
      {inst.status.warnings.length > 0 && (
        <Alert color="yellow" title="Some checks could not run">
          <List size="sm">
            {inst.status.warnings.map((w) => (
              <List.Item key={w}>{w}</List.Item>
            ))}
          </List>
        </Alert>
      )}

      {ackedCount > 0 && (
        <Switch
          label={`Show acknowledged (${ackedCount})`}
          checked={showAcked}
          onChange={(e) => setShowAcked(e.currentTarget.checked)}
        />
      )}
      <Paper withBorder>
        <TaskTable tasks={fleetTasks([inst]).filter((t) => showAcked || !isAcked(t))} showInstance={false} />
      </Paper>

      <Modal opened={bulkOpen} onClose={() => setBulkOpen(false)} title="Install updates?" centered>
        <Stack>
          <Text size="sm">The following updates will be installed on {inst.name}:</Text>
          <List size="sm">
            {bulkTasks.map((t) => (
              <List.Item key={t.id}>{t.title}</List.Item>
            ))}
          </List>
          <Text size="xs" c="dimmed">
            Core, OS and Supervisor updates are excluded: install them individually, or across instances with a rollout (the
            rocket icon on the task). A backup is created first when an update supports it.
          </Text>
          {bulk.isError && <Alert color="red">{bulk.error.message}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setBulkOpen(false)}>
              Cancel
            </Button>
            <Button onClick={() => bulk.mutate()} loading={bulk.isPending}>
              Install all
            </Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  )
}
