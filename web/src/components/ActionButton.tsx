import { useState } from 'react'
import { Alert, Button, Checkbox, Group, Modal, Stack, Text } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { useMutation } from '@tanstack/react-query'
import { api, type ActionType, type Task } from '../api'
import { actionLabels, isDisruptive } from '../labels'
import { useAIStatus, useCan } from '../hooks'
import { ReviewPanel } from './AIReview'

interface Props {
  task: Task
  action: ActionType
  instanceName: string
  running?: boolean
  disabled?: boolean
  aiEnabled?: boolean
}

/** A button that asks for confirmation before submitting an action. */
export default function ActionButton({ task, action, instanceName, running, disabled, aiEnabled }: Props) {
  const { data: ai } = useAIStatus()
  const canOperate = useCan('operator')
  const showReview = !!ai?.enabled && aiEnabled && action === 'install_update'
  const [open, setOpen] = useState(false)
  const supportsBackup = action === 'install_update' && task.source?.supports_backup === true
  const [backup, setBackup] = useState(true)
  const disruptive = isDisruptive(task, action)

  const submit = useMutation({
    mutationFn: () => api.submitAction(task.instance_id, task.id, action, supportsBackup && backup),
    onSuccess: () => {
      setOpen(false)
      notifications.show({ message: `${actionLabels[action]} started on ${instanceName}` })
    },
  })

  if (!canOperate) return null

  return (
    <>
      <Button
        size="xs"
        variant={action === 'skip_update' ? 'default' : 'light'}
        color={disruptive ? 'red' : undefined}
        loading={running}
        disabled={disabled}
        onClick={() => {
          submit.reset()
          setOpen(true)
        }}
      >
        {actionLabels[action]}
      </Button>
      <Modal opened={open} onClose={() => setOpen(false)} title={`${actionLabels[action]}?`} centered>
        <Stack>
          <Text size="sm">
            <b>{instanceName}</b>: {task.title}
          </Text>
          {open && showReview && <ReviewPanel task={task} />}
          {disruptive && (
            <Alert color="red" variant="light">
              This will restart or reboot {instanceName}. It will be unavailable for a while.
            </Alert>
          )}
          {supportsBackup && (
            <Checkbox
              label="Create a backup before updating"
              checked={backup}
              onChange={(e) => setBackup(e.currentTarget.checked)}
            />
          )}
          {submit.isError && <Alert color="red">{submit.error.message}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button color={disruptive ? 'red' : undefined} loading={submit.isPending} onClick={() => submit.mutate()}>
              {actionLabels[action]}
            </Button>
          </Group>
        </Stack>
      </Modal>
    </>
  )
}
