import { Badge, Tooltip } from '@mantine/core'
import type { InstanceStatus } from '../api'

const colors: Record<InstanceStatus['state'], string> = {
  connected: 'green',
  connecting: 'yellow',
  error: 'red',
  disabled: 'gray',
}

export default function StatusBadge({ status }: { status: InstanceStatus }) {
  const badge = (
    <Badge color={colors[status.state]} variant="dot">
      {status.state}
    </Badge>
  )
  return status.error ? (
    <Tooltip label={status.error} multiline w={320}>
      {badge}
    </Tooltip>
  ) : (
    badge
  )
}
