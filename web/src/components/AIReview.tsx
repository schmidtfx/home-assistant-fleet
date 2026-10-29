import { Alert, Badge, Button, Group, List, Loader, Stack, Text, Tooltip } from '@mantine/core'
import { IconSparkles } from '@tabler/icons-react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api, type Review, type Task } from '../api'
import { useAIReviews, useCan } from '../hooks'

export const riskColor: Record<Review['risk'], string> = { low: 'green', review: 'yellow', high: 'red' }

/** Small badge showing the cached AI risk for an update task (renders nothing if none). */
export function RiskBadge({ taskId }: { taskId: string }) {
  const { data } = useAIReviews()
  const r = data?.[taskId]
  if (!r) return null
  return (
    <Tooltip label={r.summary[0] ?? 'AI review'} multiline w={300}>
      <Badge size="xs" variant="light" color={riskColor[r.risk]} leftSection={<IconSparkles size={10} />}>
        {r.risk} risk
      </Badge>
    </Tooltip>
  )
}

/** AI review for an update task, with a button to generate or refresh it. */
export function ReviewPanel({ task }: { task: Task }) {
  const qc = useQueryClient()
  const { data: reviews } = useAIReviews()
  const review = reviews?.[task.id]
  const canOperate = useCan('operator')
  const run = useMutation({
    mutationFn: (refresh: boolean) => api.aiReview(task.instance_id, task.id, refresh),
    onSuccess: (r) => qc.setQueryData<Record<string, Review>>(['ai-reviews'], (old) => ({ ...(old ?? {}), [task.id]: r })),
  })

  if (!review) {
    if (!canOperate) return null
    return (
      <Stack gap="xs">
        <Button
          size="xs"
          variant="light"
          leftSection={<IconSparkles size={14} />}
          loading={run.isPending}
          onClick={() => run.mutate(false)}
        >
          Review breaking changes for this instance
        </Button>
        {run.isPending && (
          <Text size="xs" c="dimmed">
            Reading the release notes… this can take a minute.
          </Text>
        )}
        {run.isError && <Alert color="red">{run.error.message}</Alert>}
      </Stack>
    )
  }

  return (
    <Alert
      color={riskColor[review.risk]}
      variant="light"
      title={
        <Group gap="xs">
          <IconSparkles size={16} />
          <span>AI review: {review.risk} risk</span>
        </Group>
      }
    >
      <Stack gap={6}>
        {review.summary.map((s) => (
          <Text key={s} size="sm">
            {s}
          </Text>
        ))}
        {review.affected.length > 0 && (
          <List size="sm" spacing={4}>
            {review.affected.map((a) => (
              <List.Item key={a.integration + a.change}>
                <b>{a.integration}</b>: {a.change}
                {a.action_needed && a.action_needed !== 'none' && <Text size="xs">→ {a.action_needed}</Text>}
              </List.Item>
            ))}
          </List>
        )}
        {!review.notes_complete && (
          <Text size="xs" c="orange">
            Release notes were incomplete or unavailable — check them yourself.
          </Text>
        )}
        <Group justify="space-between">
          <Text size="xs" c="dimmed">
            {review.model} · AI-generated, may be wrong
          </Text>
          {canOperate && (
            <Button size="compact-xs" variant="subtle" loading={run.isPending} onClick={() => run.mutate(true)}>
              {run.isPending ? <Loader size="xs" /> : 'Refresh'}
            </Button>
          )}
        </Group>
        {run.isError && <Alert color="red">{run.error.message}</Alert>}
      </Stack>
    </Alert>
  )
}
