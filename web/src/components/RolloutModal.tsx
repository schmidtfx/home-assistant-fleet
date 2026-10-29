import { useMemo, useState } from 'react'
import {
  ActionIcon,
  Alert,
  Badge,
  Button,
  Checkbox,
  Group,
  Modal,
  NumberInput,
  Paper,
  Stack,
  Text,
  TextInput,
} from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconArrowDown, IconArrowUp } from '@tabler/icons-react'
import { useMutation } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { api, type Instance, type Task } from '../api'
import { useAIStatus, useInstances } from '../hooks'
import { isAcked } from '../labels'
import { RiskBadge } from './AIReview'

export interface Candidate {
  instance: Instance
  task: Task
}

/** Instances that offer the same update (entity and version) and can install it. */
export function rolloutCandidates(instances: Instance[] | undefined, task: Task): Candidate[] {
  const entity = task.source?.entity_id
  const version = task.source?.latest_version
  if (task.kind !== 'update' || !entity) return []
  const out: Candidate[] = []
  for (const instance of instances ?? []) {
    const t = instance.tasks.find(
      (x) => x.kind === 'update' && x.source?.entity_id === entity && x.source?.latest_version === version,
    )
    if (t && t.actions.includes('install_update')) out.push({ instance, task: t })
  }
  return out
}

interface Row extends Candidate {
  included: boolean
}

export default function RolloutModal({ task, opened, onClose }: { task: Task; opened: boolean; onClose: () => void }) {
  const { data: instances } = useInstances()
  const { data: ai } = useAIStatus()
  const navigate = useNavigate()
  const candidates = useMemo(() => rolloutCandidates(instances, task), [instances, task])
  // Acknowledged updates start unchecked: someone chose to hold them back.
  const [rows, setRows] = useState<Row[]>(() => candidates.map((c) => ({ ...c, included: !isAcked(c.task) })))
  const [canaries, setCanaries] = useState(1)
  const [gate, setGate] = useState(15)
  const [backup, setBackup] = useState(true)
  const [startAt, setStartAt] = useState('')
  const [aiGate, setAiGate] = useState(false)

  const included = rows.filter((r) => r.included)
  const canaryCount = Math.min(canaries, included.length)
  const anyBackup = included.some((r) => r.task.source?.supports_backup === true)

  const move = (i: number, d: -1 | 1) =>
    setRows((rs) => {
      const out = [...rs]
      ;[out[i], out[i + d]] = [out[i + d], out[i]]
      return out
    })

  const create = useMutation({
    mutationFn: () =>
      api.createRollout({
        entity_id: String(task.source?.entity_id),
        target_version: String(task.source?.latest_version),
        instances: included.map((r) => r.instance.id),
        canary_count: canaryCount,
        backup: anyBackup && backup,
        gate_minutes: gate,
        ai_gate: !!ai?.enabled && aiGate,
        start_at: startAt ? new Date(startAt).toISOString() : undefined,
      }),
    onSuccess: () => {
      onClose()
      notifications.show({ message: `Rollout started on ${included.length} instance${included.length > 1 ? 's' : ''}` })
      navigate('/rollouts')
    },
  })

  let canaryIndex = 0
  return (
    <Modal opened={opened} onClose={onClose} title="Roll out to fleet" size="lg" centered>
      <Stack>
        <Text size="sm">
          <b>{task.title}</b>
        </Text>
        <Text size="xs" c="dimmed">
          Instances are updated one at a time in this order. After each install hafleet waits for the new version and
          then watches the instance for the health check period; a new critical task pauses the rollout. Canaries go
          first. Instances with a maintenance window are only updated inside it.
        </Text>

        <Stack gap={6}>
          {rows.map((r, i) => {
            const canary = r.included && canaryIndex++ < canaryCount
            return (
              <Paper key={r.instance.id} withBorder p="xs">
                <Group justify="space-between" wrap="nowrap">
                  <Group gap="sm" wrap="nowrap">
                    <Checkbox
                      aria-label={`Include ${r.instance.name}`}
                      checked={r.included}
                      onChange={(e) => {
                        const v = e.currentTarget.checked
                        setRows((rs) => rs.map((x, j) => (j === i ? { ...x, included: v } : x)))
                      }}
                    />
                    <div>
                      <Group gap={6}>
                        <Text size="sm" fw={500}>
                          {r.instance.name}
                        </Text>
                        {canary && (
                          <Badge size="xs" variant="light">
                            canary
                          </Badge>
                        )}
                        {isAcked(r.task) && (
                          <Badge size="xs" color="gray" variant="outline">
                            acknowledged
                          </Badge>
                        )}
                        {ai?.enabled && r.instance.ai_enabled && <RiskBadge taskId={r.task.id} />}
                      </Group>
                      <Text size="xs" c="dimmed">
                        {r.task.detail}
                        {r.instance.maint_window && ` · window ${r.instance.maint_window}`}
                      </Text>
                    </div>
                  </Group>
                  <Group gap={2} wrap="nowrap">
                    <ActionIcon variant="subtle" aria-label="Move up" disabled={i === 0} onClick={() => move(i, -1)}>
                      <IconArrowUp size={16} />
                    </ActionIcon>
                    <ActionIcon
                      variant="subtle"
                      aria-label="Move down"
                      disabled={i === rows.length - 1}
                      onClick={() => move(i, 1)}
                    >
                      <IconArrowDown size={16} />
                    </ActionIcon>
                  </Group>
                </Group>
              </Paper>
            )
          })}
        </Stack>

        <Group grow align="flex-start">
          <NumberInput
            label="Canaries"
            description="Updated first"
            min={0}
            max={Math.max(included.length, 0)}
            value={canaryCount}
            onChange={(v) => setCanaries(Number(v) || 0)}
          />
          <NumberInput
            label="Health check (minutes)"
            description="Watched after each install"
            min={0}
            max={1440}
            value={gate}
            onChange={(v) => setGate(Number(v) || 0)}
          />
          <TextInput
            label="Start"
            description="Empty = now"
            type="datetime-local"
            value={startAt}
            onChange={(e) => setStartAt(e.currentTarget.value)}
          />
        </Group>
        {anyBackup && (
          <Checkbox
            label="Create a backup before each update (where supported)"
            checked={backup}
            onChange={(e) => setBackup(e.currentTarget.checked)}
          />
        )}
        {ai?.enabled && (
          <Checkbox
            label="Also ask the AI to check each instance after the update"
            description="Looks at new tasks and error logs since the install. A doubtful or bad verdict pauses the rollout. Instances without AI enabled are not sent and skip this check."
            checked={aiGate}
            onChange={(e) => setAiGate(e.currentTarget.checked)}
          />
        )}
        {create.isError && <Alert color="red">{create.error.message}</Alert>}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={included.length === 0} loading={create.isPending} onClick={() => create.mutate()}>
            Start rollout on {included.length} instance{included.length === 1 ? '' : 's'}
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}
