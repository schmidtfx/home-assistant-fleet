import { useState } from 'react'
import { ActionIcon, Alert, Anchor, Button, Group, Loader, Modal, Paper, Stack, Table, Text, Title, Tooltip } from '@mantine/core'
import { IconLock, IconLockOpen, IconPencil, IconPlus, IconTrash } from '@tabler/icons-react'
import { useMutation } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { api, type Instance } from '../api'
import InstanceForm from '../components/InstanceForm'
import StatusBadge from '../components/StatusBadge'
import { useCan, useInstances } from '../hooks'

export default function Instances() {
  const { data: instances, isPending, isError, error } = useInstances()
  const isAdmin = useCan('admin')
  const [editing, setEditing] = useState<Instance | 'new' | null>(null)
  const [deleting, setDeleting] = useState<Instance | null>(null)
  const del = useMutation({
    mutationFn: (id: string) => api.deleteInstance(id),
    onSuccess: () => setDeleting(null),
  })

  if (isPending) return <Loader />
  if (isError) return <Alert color="red">{error.message}</Alert>

  return (
    <Stack>
      <Group justify="space-between">
        <Title order={2}>Instances</Title>
        {isAdmin && (
          <Button leftSection={<IconPlus size={16} />} onClick={() => setEditing('new')}>
            Add instance
          </Button>
        )}
      </Group>

      <Paper withBorder>
        <Table verticalSpacing="sm">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>URL</Table.Th>
              <Table.Th>TLS</Table.Th>
              <Table.Th>Status</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {instances.map((i) => (
              <Table.Tr key={i.id}>
                <Table.Td>
                  <Anchor component={Link} to={`/instances/${i.id}`}>
                    {i.name}
                  </Anchor>
                </Table.Td>
                <Table.Td>
                  <Text size="sm">{i.url}</Text>
                </Table.Td>
                <Table.Td>
                  {i.url.startsWith('http://') ? (
                    <Tooltip label="Plain HTTP: token is sent unencrypted">
                      <IconLockOpen size={18} color="var(--mantine-color-red-6)" />
                    </Tooltip>
                  ) : (
                    <Tooltip
                      label={i.cert_fingerprint ? 'Certificate pinned' : i.has_ca_pem ? 'Custom CA' : 'System trust store'}
                    >
                      <IconLock size={18} color="var(--mantine-color-green-6)" />
                    </Tooltip>
                  )}
                </Table.Td>
                <Table.Td>
                  <StatusBadge status={i.status} />
                </Table.Td>
                <Table.Td>
                  <Group gap="xs" justify="flex-end" display={isAdmin ? undefined : 'none'}>
                    <ActionIcon variant="subtle" aria-label="Edit" onClick={() => setEditing(i)}>
                      <IconPencil size={16} />
                    </ActionIcon>
                    <ActionIcon variant="subtle" color="red" aria-label="Delete" onClick={() => setDeleting(i)}>
                      <IconTrash size={16} />
                    </ActionIcon>
                  </Group>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
        {instances.length === 0 && (
          <Text c="dimmed" ta="center" py="xl">
            No instances configured.
          </Text>
        )}
      </Paper>

      <Modal
        opened={editing !== null}
        onClose={() => setEditing(null)}
        title={editing === 'new' ? 'Add instance' : `Edit ${editing?.name ?? ''}`}
        size="lg"
      >
        {editing !== null && (
          <InstanceForm
            instance={editing === 'new' ? undefined : editing}
            onSaved={() => setEditing(null)}
            onCancel={() => setEditing(null)}
          />
        )}
      </Modal>

      <Modal opened={deleting !== null} onClose={() => setDeleting(null)} title="Delete instance?" centered>
        <Stack>
          <Text size="sm">
            Remove <b>{deleting?.name}</b> from the fleet? The stored access token is deleted. You may also want to revoke the
            token in Home Assistant.
          </Text>
          {del.isError && <Alert color="red">{del.error.message}</Alert>}
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setDeleting(null)}>
              Cancel
            </Button>
            <Button color="red" loading={del.isPending} onClick={() => deleting && del.mutate(deleting.id)}>
              Delete
            </Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  )
}
