import { Alert, Badge, Button, Card, Group, Loader, SimpleGrid, Stack, Text, Title } from '@mantine/core'
import { Link } from 'react-router-dom'
import type { Instance, Severity } from '../api'
import BriefingCard from '../components/BriefingCard'
import StatusBadge from '../components/StatusBadge'
import { useAIStatus, useInstances } from '../hooks'
import { isAcked, relativeTime, severityColor } from '../labels'

// Acknowledged tasks are left out of all counts.
const open = (inst: Instance) => inst.tasks.filter((t) => !isAcked(t))

function counts(inst: Instance): Record<Severity, number> {
  const c: Record<Severity, number> = { critical: 0, warning: 0, info: 0 }
  for (const t of open(inst)) c[t.severity]++
  return c
}

export default function Dashboard() {
  const { data: instances, isPending, isError, error } = useInstances()
  const { data: ai } = useAIStatus()
  if (isPending) return <Loader />
  if (isError) return <Alert color="red">{error.message}</Alert>

  const total = instances.reduce((n, i) => n + open(i).length, 0)
  const updates = instances.reduce((n, i) => n + open(i).filter((t) => t.kind === 'update').length, 0)

  return (
    <Stack>
      <Group justify="space-between">
        <div>
          <Title order={2}>Fleet</Title>
          <Text c="dimmed" size="sm">
            {instances.length} instances · {total} open tasks · {updates} pending updates
          </Text>
        </div>
        <Button component={Link} to="/tasks" variant="light">
          View all tasks
        </Button>
      </Group>

      {instances.length === 0 && (
        <Alert title="No instances yet">
          Add your first Home Assistant instance on the{' '}
          <Text component={Link} to="/instances" c="blue" inherit>
            Instances
          </Text>{' '}
          page.
        </Alert>
      )}

      {ai?.enabled && instances.some((i) => i.ai_enabled) && <BriefingCard instances={instances} />}

      <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }}>
        {instances.map((inst) => {
          const c = counts(inst)
          const acked = inst.tasks.length - open(inst).length
          return (
            <Card key={inst.id} withBorder component={Link} to={`/instances/${inst.id}`} style={{ textDecoration: 'none' }}>
              <Group justify="space-between" mb="xs">
                <Text fw={600}>{inst.name}</Text>
                <StatusBadge status={inst.status} />
              </Group>
              <Text size="xs" c="dimmed" truncate>
                {inst.url}
              </Text>
              <Text size="xs" c="dimmed">
                {inst.status.ha_version ? `Home Assistant ${inst.status.ha_version}` : '—'}
                {inst.status.last_sync && ` · synced ${relativeTime(inst.status.last_sync)}`}
              </Text>
              <Group gap="xs" mt="md">
                {open(inst).length === 0 ? (
                  <Badge color="green" variant="light">
                    all good
                  </Badge>
                ) : (
                  (Object.keys(c) as Severity[])
                    .filter((s) => c[s] > 0)
                    .map((s) => (
                      <Badge key={s} color={severityColor[s]} variant="light">
                        {c[s]} {s}
                      </Badge>
                    ))
                )}
                {acked > 0 && (
                  <Badge color="gray" variant="outline">
                    {acked} acknowledged
                  </Badge>
                )}
                {inst.status.warnings.length > 0 && (
                  <Badge color="gray" variant="outline">
                    {inst.status.warnings.length} warning{inst.status.warnings.length > 1 ? 's' : ''}
                  </Badge>
                )}
              </Group>
            </Card>
          )
        })}
      </SimpleGrid>
    </Stack>
  )
}
