import { useState } from 'react'
import { Alert, Anchor, Badge, Button, Group, Loader, Modal, Paper, Stack, Table, Text, Title } from '@mantine/core'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { api, type Rollout } from '../api'
import { useAIStatus, useCan, useInstances } from '../hooks'
import HealthCheckModal from '../components/HealthCheckModal'
import { relativeTime, rolloutStateColor, stepStateColor } from '../labels'

function RolloutCard({ r }: { r: Rollout }) {
  const qc = useQueryClient()
  const { data: instances } = useInstances()
  const canOperate = useCan('operator')
  const [confirmCancel, setConfirmCancel] = useState(false)
  const [checkStep, setCheckStep] = useState<{ instanceId: string; actionId: string } | null>(null)
  const { data: ai } = useAIStatus()
  const aiFor = (id: string) => !!ai?.enabled && !!instances?.find((i) => i.id === id)?.ai_enabled
  const name = (id: string) => instances?.find((i) => i.id === id)?.name ?? id
  const replace = (updated: Rollout) =>
    qc.setQueryData<Rollout[]>(['rollouts'], (old) => old?.map((x) => (x.id === updated.id ? updated : x)))
  const control = useMutation({
    mutationFn: (op: 'pause' | 'resume' | 'cancel') => api.rolloutControl(r.id, op),
    onSuccess: (updated) => {
      replace(updated)
      setConfirmCancel(false)
    },
  })
  const skip = useMutation({ mutationFn: (ord: number) => api.skipRolloutStep(r.id, ord), onSuccess: replace })
  const active = r.state === 'running' || r.state === 'paused'
  const done = r.steps.filter((s) => s.state === 'done' || s.state === 'skipped').length

  return (
    <Paper withBorder p="md">
      <Group justify="space-between" align="flex-start">
        <div>
          <Group gap="xs">
            <Text fw={600}>{r.title}</Text>
            <Badge color={rolloutStateColor[r.state]} variant="light">
              {r.state}
            </Badge>
          </Group>
          <Text size="xs" c="dimmed">
            {r.entity_id} → {r.target_version} · started by {r.created_by} {relativeTime(r.created_at)} · {done}/
            {r.steps.length} done · health check {r.gate_minutes} min{r.ai_gate ? ' + AI' : ''}
            {r.backup ? ' · with backup' : ''}
            {r.start_at && ` · starts ${new Date(r.start_at).toLocaleString()}`}
          </Text>
        </div>
        {canOperate && active && (
          <Group gap="xs">
            {r.state === 'running' && (
              <Button size="xs" variant="default" loading={control.isPending} onClick={() => control.mutate('pause')}>
                Pause
              </Button>
            )}
            {r.state === 'paused' && (
              <Button size="xs" loading={control.isPending} onClick={() => control.mutate('resume')}>
                Resume
              </Button>
            )}
            <Button size="xs" variant="subtle" color="red" onClick={() => setConfirmCancel(true)}>
              Cancel
            </Button>
          </Group>
        )}
      </Group>
      {r.message && r.state === 'paused' && (
        <Alert color="orange" mt="sm" p="xs">
          {r.message}. Resume retries a failed step; skip it to continue with the next instance.
        </Alert>
      )}
      {(control.isError || skip.isError) && (
        <Alert color="red" mt="sm" p="xs">
          {(control.error ?? skip.error)?.message}
        </Alert>
      )}
      <Table mt="sm" verticalSpacing={4}>
        <Table.Thead>
          <Table.Tr>
            <Table.Th w={30}>#</Table.Th>
            <Table.Th>Instance</Table.Th>
            <Table.Th>State</Table.Th>
            <Table.Th>Details</Table.Th>
            <Table.Th />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {r.steps.map((s) => (
            <Table.Tr key={s.ord}>
              <Table.Td>{s.ord + 1}</Table.Td>
              <Table.Td>
                <Group gap={6}>
                  <Anchor component={Link} to={`/instances/${s.instance_id}`} size="sm">
                    {name(s.instance_id)}
                  </Anchor>
                  {s.canary && (
                    <Badge size="xs" variant="light">
                      canary
                    </Badge>
                  )}
                </Group>
              </Table.Td>
              <Table.Td>
                <Badge color={stepStateColor[s.state]} variant={s.state === 'done' ? 'filled' : 'light'}>
                  {s.state}
                </Badge>
              </Table.Td>
              <Table.Td>
                <Text size="xs" c={s.state === 'failed' ? 'red' : 'dimmed'}>
                  {s.message}
                  {!s.message && s.finished_at && `finished ${relativeTime(s.finished_at)}`}
                </Text>
              </Table.Td>
              <Table.Td ta="right">
                {canOperate && s.action_id && (s.state === 'done' || s.state === 'failed') && aiFor(s.instance_id) && (
                  <Button
                    size="compact-xs"
                    variant="subtle"
                    onClick={() => setCheckStep({ instanceId: s.instance_id, actionId: s.action_id! })}
                  >
                    AI check
                  </Button>
                )}
                {canOperate && r.state === 'paused' && (s.state === 'failed' || s.state === 'pending') && (
                  <Button size="compact-xs" variant="subtle" loading={skip.isPending} onClick={() => skip.mutate(s.ord)}>
                    Skip
                  </Button>
                )}
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      <HealthCheckModal
        instanceId={checkStep?.instanceId ?? ''}
        actionId={checkStep?.actionId ?? null}
        title={`AI check: ${checkStep ? name(checkStep.instanceId) : ''}`}
        onClose={() => setCheckStep(null)}
      />
      <Modal opened={confirmCancel} onClose={() => setConfirmCancel(false)} title="Cancel rollout?" centered>
        <Stack>
          <Text size="sm">
            Instances that have not started are skipped. An install that is already running in Home Assistant cannot be
            stopped.
          </Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setConfirmCancel(false)}>
              Keep running
            </Button>
            <Button color="red" loading={control.isPending} onClick={() => control.mutate('cancel')}>
              Cancel rollout
            </Button>
          </Group>
        </Stack>
      </Modal>
    </Paper>
  )
}

export default function Rollouts() {
  const q = useQuery({ queryKey: ['rollouts'], queryFn: api.rollouts, staleTime: 30_000 })
  if (q.isPending) return <Loader />
  if (q.isError) return <Alert color="red">{q.error.message}</Alert>
  return (
    <Stack>
      <div>
        <Title order={2}>Rollouts</Title>
        <Text size="sm" c="dimmed">
          Install an update across instances one at a time, canaries first, with a health check after each. Start one
          from an update task with “Roll out”.
        </Text>
      </div>
      {q.data.length === 0 && (
        <Text c="dimmed" ta="center" py="xl">
          No rollouts yet.
        </Text>
      )}
      {q.data.map((r) => (
        <RolloutCard key={r.id} r={r} />
      ))}
    </Stack>
  )
}
