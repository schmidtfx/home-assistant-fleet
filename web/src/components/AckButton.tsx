import { useState } from 'react'
import { ActionIcon, Alert, Button, Group, Modal, SegmentedControl, Stack, Text, Textarea, Tooltip } from '@mantine/core'
import { IconEye, IconEyeOff } from '@tabler/icons-react'
import { useMutation } from '@tanstack/react-query'
import { api, type Task } from '../api'
import { useCan } from '../hooks'
import { isAcked } from '../labels'

const durations = [
  { value: '0', label: 'Until it changes' },
  { value: '24', label: '1 day' },
  { value: '168', label: '7 days' },
  { value: '720', label: '30 days' },
]

function ackLabel(t: Task): string {
  const a = t.ack!
  let s = `Acknowledged by ${a.by} on ${new Date(a.at).toLocaleString()}`
  s += a.expires_at ? `, until ${new Date(a.expires_at).toLocaleString()}` : ', until it changes or resolves'
  return a.note ? `${s}. Note: ${a.note}` : s
}

/** Hides a task (with an optional note and snooze), or shows an acknowledged task again. */
export default function AckButton({ task, instanceName }: { task: Task; instanceName: string }) {
  const [open, setOpen] = useState(false)
  const [note, setNote] = useState('')
  const [hours, setHours] = useState('0')
  const ack = useMutation({
    mutationFn: () => api.ack(task.instance_id, task.id, note, Number(hours)),
    onSuccess: () => setOpen(false),
  })
  const unack = useMutation({ mutationFn: () => api.unack(task.instance_id, task.id) })
  const canOperate = useCan('operator')

  if (!canOperate) {
    // Viewers see who acknowledged a task but cannot change it.
    return isAcked(task) ? (
      <Tooltip label={ackLabel(task)} multiline w={320}>
        <ActionIcon variant="subtle" color="gray" aria-label="Acknowledged" component="span">
          <IconEyeOff size={18} />
        </ActionIcon>
      </Tooltip>
    ) : null
  }

  if (isAcked(task)) {
    return (
      <Tooltip label={`${ackLabel(task)}. Click to show it again.`} multiline w={320}>
        <ActionIcon variant="subtle" color="gray" aria-label="Unacknowledge" loading={unack.isPending} onClick={() => unack.mutate()}>
          <IconEye size={18} />
        </ActionIcon>
      </Tooltip>
    )
  }

  return (
    <>
      <Tooltip label="Acknowledge and hide">
        <ActionIcon
          variant="subtle"
          color="gray"
          aria-label="Acknowledge"
          onClick={() => {
            setNote('')
            setHours('0')
            ack.reset()
            setOpen(true)
          }}
        >
          <IconEyeOff size={18} />
        </ActionIcon>
      </Tooltip>
      <Modal opened={open} onClose={() => setOpen(false)} title="Acknowledge task" centered>
        <Stack>
          <Text size="sm">
            <b>{instanceName}</b>: {task.title}
          </Text>
          <Text size="xs" c="dimmed">
            The task is hidden from lists and counts. It comes back automatically if it changes (for example a newer
            version or a different error state), if it resolves and later recurs, or when the snooze ends.
          </Text>
          <SegmentedControl data={durations} value={hours} onChange={setHours} fullWidth />
          <Textarea
            label="Note (optional)"
            placeholder="e.g. printer is switched off; waiting for upstream fix"
            maxLength={500}
            autosize
            minRows={2}
            value={note}
            onChange={(e) => setNote(e.currentTarget.value)}
          />
          {ack.isError && <Alert color="red">{ack.error.message}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button loading={ack.isPending} onClick={() => ack.mutate()}>
              Acknowledge
            </Button>
          </Group>
        </Stack>
      </Modal>
    </>
  )
}
