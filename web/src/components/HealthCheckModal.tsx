import { useState } from 'react'
import { Alert, Anchor, Badge, Button, Group, List, Loader, Modal, Stack, Text, Title } from '@mantine/core'
import { useQuery } from '@tanstack/react-query'
import { api } from '../api'

const verdictColor = { ok: 'green', suspect: 'orange', broken: 'red' } as const
const verdictLabel = { ok: 'No problems found', suspect: 'Possibly affected', broken: 'Likely broken' } as const

/** AI check of whether a finished update install broke something. */
export default function HealthCheckModal({
  instanceId,
  actionId,
  title,
  onClose,
}: {
  instanceId: string
  actionId: string | null
  title: string
  onClose: () => void
}) {
  const [refresh, setRefresh] = useState(0)
  const q = useQuery({
    queryKey: ['ai-health', instanceId, actionId, refresh],
    queryFn: () => api.aiHealth(instanceId, actionId!, refresh > 0),
    enabled: !!actionId,
    staleTime: Infinity,
    retry: false,
  })
  return (
    <Modal opened={!!actionId} onClose={onClose} title={title} size="lg" centered>
      <Stack>
        {q.isPending && (
          <Group gap="xs">
            <Loader size="sm" />
            <Text size="sm" c="dimmed">
              Checking new tasks and error logs since the update…
            </Text>
          </Group>
        )}
        {q.isError && <Alert color="red">{q.error.message}</Alert>}
        {q.data && (
          <>
            <Badge color={verdictColor[q.data.verdict]} variant="light" size="lg">
              {verdictLabel[q.data.verdict]}
            </Badge>
            {q.data.findings.length > 0 && (
              <>
                <Title order={5}>Findings</Title>
                <List size="sm" spacing={4}>
                  {q.data.findings.map((f) => (
                    <List.Item key={f}>{f}</List.Item>
                  ))}
                </List>
              </>
            )}
            {q.data.steps.length > 0 && (
              <>
                <Title order={5}>Next steps</Title>
                <List type="ordered" size="sm" spacing={4}>
                  {q.data.steps.map((s) => (
                    <List.Item key={s}>{s}</List.Item>
                  ))}
                </List>
              </>
            )}
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
                Check again
              </Button>
            </Group>
          </>
        )}
      </Stack>
    </Modal>
  )
}
