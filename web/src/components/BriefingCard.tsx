import { Alert, Anchor, Button, Card, Group, List, Stack, Text, Title } from '@mantine/core'
import { IconSparkles } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { api, type Instance } from '../api'
import { useCan } from '../hooks'
import { relativeTime } from '../labels'

export default function BriefingCard({ instances }: { instances: Instance[] }) {
  const qc = useQueryClient()
  const canOperate = useCan('operator')
  const q = useQuery({ queryKey: ['briefing'], queryFn: api.briefing })
  const gen = useMutation({
    mutationFn: api.newBriefing,
    onSuccess: (data) => qc.setQueryData(['briefing'], data),
  })
  const b = q.data?.briefing
  const inst = (id: string) => instances.find((i) => i.id === id)
  const taskTitle = (iid: string, tid: string) => inst(iid)?.tasks.find((t) => t.id === tid)?.title ?? 'task resolved'

  return (
    <Card withBorder>
      <Group justify="space-between" mb="xs">
        <Group gap="xs">
          <IconSparkles size={18} />
          <Title order={4}>Briefing</Title>
        </Group>
        {canOperate && (
          <Button size="xs" variant="light" loading={gen.isPending} onClick={() => gen.mutate()}>
            {b ? 'Refresh' : 'Generate'}
          </Button>
        )}
      </Group>
      {gen.isError && <Alert color="red">{gen.error.message}</Alert>}
      {!b && !gen.isPending && (
        <Text size="sm" c="dimmed">
          Get an AI summary of what needs attention across instances with AI enabled.
        </Text>
      )}
      {b && (
        <Stack gap="sm">
          <Text fw={500}>{b.headline}</Text>
          {b.urgent.length > 0 && (
            <div>
              <Text size="sm" fw={600}>
                Urgent
              </Text>
              <List size="sm" spacing={4}>
                {b.urgent.map((u) => (
                  <List.Item key={u.instance_id + u.task_id}>
                    <Anchor component={Link} to={`/instances/${u.instance_id}`} size="sm">
                      {inst(u.instance_id)?.name}
                    </Anchor>
                    : {taskTitle(u.instance_id, u.task_id)} — {u.why}
                  </List.Item>
                ))}
              </List>
            </div>
          )}
          {b.suggested_order.length > 0 && (
            <div>
              <Text size="sm" fw={600}>
                Suggested order
              </Text>
              <List type="ordered" size="sm" spacing={4}>
                {b.suggested_order.map((st, i) => (
                  <List.Item key={i}>
                    <Anchor component={Link} to={`/instances/${st.instance_id}`} size="sm">
                      {inst(st.instance_id)?.name}
                    </Anchor>
                    {st.task_ids.length > 0 && `: ${st.task_ids.map((t) => taskTitle(st.instance_id, t)).join(', ')}`} — {st.reason}
                  </List.Item>
                ))}
              </List>
            </div>
          )}
          {b.notes && <Text size="sm">{b.notes}</Text>}
          <Text size="xs" c="dimmed">
            {b.model} · {relativeTime(b.created_at)} · AI-generated, may be wrong
          </Text>
        </Stack>
      )}
    </Card>
  )
}
