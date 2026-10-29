import { Alert, Anchor, Badge, Button, Drawer, Group, List, Loader, Stack, Text, Title } from '@mantine/core'
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { api, type Task } from '../api'

const urgencyColor = { low: 'blue', medium: 'orange', high: 'red' } as const

export default function ExplainDrawer({ task, onClose }: { task: Task | null; onClose: () => void }) {
  const [refresh, setRefresh] = useState(0)
  const q = useQuery({
    queryKey: ['ai-explain', task?.instance_id, task?.id, refresh],
    queryFn: () => api.aiExplain(task!.instance_id, task!.id, refresh > 0),
    enabled: !!task,
    staleTime: Infinity,
    retry: false,
  })

  return (
    <Drawer opened={!!task} onClose={onClose} position="right" size="lg" title={task?.title}>
      <Stack>
        {q.isPending && (
          <Group gap="xs">
            <Loader size="sm" />
            <Text size="sm" c="dimmed">
              Asking the AI…
            </Text>
          </Group>
        )}
        {q.isError && <Alert color="red">{q.error.message}</Alert>}
        {q.data && (
          <>
            <Group gap="xs">
              <Title order={5}>Likely cause</Title>
              <Badge color={urgencyColor[q.data.urgency]} variant="light">
                {q.data.urgency} urgency
              </Badge>
            </Group>
            <Text size="sm">{q.data.likely_cause}</Text>
            <Title order={5}>How to fix</Title>
            <List type="ordered" size="sm" spacing={6}>
              {q.data.steps.map((s) => (
                <List.Item key={s}>{s}</List.Item>
              ))}
            </List>
            {q.data.docs_url && (
              <Anchor href={q.data.docs_url} target="_blank" rel="noopener noreferrer" size="sm">
                Documentation
              </Anchor>
            )}
            <Group justify="space-between">
              <Text size="xs" c="dimmed">
                {q.data.model} · AI-generated, may be wrong
              </Text>
              <Button size="compact-xs" variant="subtle" onClick={() => setRefresh((n) => n + 1)}>
                Regenerate
              </Button>
            </Group>
          </>
        )}
      </Stack>
    </Drawer>
  )
}
