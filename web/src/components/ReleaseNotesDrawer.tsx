import { Alert, Anchor, Drawer, Loader, Stack, Text, Typography } from '@mantine/core'
import { useQuery } from '@tanstack/react-query'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { api, type Task } from '../api'
import { ReviewPanel } from './AIReview'

interface Props {
  task: Task | null
  aiEnabled?: boolean
  onClose: () => void
}

export default function ReleaseNotesDrawer({ task, aiEnabled, onClose }: Props) {
  const q = useQuery({
    queryKey: ['release-notes', task?.instance_id, task?.id],
    queryFn: () => api.releaseNotes(task!.instance_id, task!.id),
    enabled: !!task,
    staleTime: 10 * 60_000,
  })
  const releaseUrl = (q.data?.release_url || task?.source?.release_url) as string | undefined

  return (
    <Drawer opened={!!task} onClose={onClose} position="right" size="lg" title={task?.title}>
      <Stack>
        {task && aiEnabled && <ReviewPanel task={task} />}
        {releaseUrl && (
          <Anchor href={releaseUrl} target="_blank" rel="noopener noreferrer">
            Full release notes
          </Anchor>
        )}
        {q.isPending && <Loader size="sm" />}
        {q.isError && <Alert color="red">{q.error.message}</Alert>}
        {q.isSuccess && !q.data.notes && (
          <Text c="dimmed" size="sm">
            No release notes available from Home Assistant.
          </Text>
        )}
        {q.data?.notes && (
          <Typography>
            {/* react-markdown does not render raw HTML, so notes cannot inject markup. */}
            <Markdown remarkPlugins={[remarkGfm]}>{q.data.notes}</Markdown>
          </Typography>
        )}
      </Stack>
    </Drawer>
  )
}
