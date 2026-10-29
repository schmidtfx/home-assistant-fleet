import { Alert, Badge, Loader, Paper, Stack, Table, Text, Title } from '@mantine/core'
import { useQuery } from '@tanstack/react-query'
import { api } from '../api'
import { actionLabel } from '../labels'

const resultColor: Record<string, string> = { succeeded: 'green', failed: 'red', submitted: 'blue', ok: 'gray' }

export default function Audit() {
  const { data, isPending, isError, error } = useQuery({ queryKey: ['audit'], queryFn: () => api.audit() })
  if (isPending) return <Loader />
  if (isError) return <Alert color="red">{error.message}</Alert>

  return (
    <Stack>
      <Title order={2}>Audit log</Title>
      <Paper withBorder>
        <Table.ScrollContainer minWidth={700}>
          <Table verticalSpacing="xs">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Time</Table.Th>
                <Table.Th>User</Table.Th>
                <Table.Th>Instance</Table.Th>
                <Table.Th>Action</Table.Th>
                <Table.Th>Target</Table.Th>
                <Table.Th>Result</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {data.map((e) => (
                <Table.Tr key={e.id}>
                  <Table.Td>
                    <Text size="sm">{new Date(e.time).toLocaleString()}</Text>
                  </Table.Td>
                  <Table.Td>{e.username}</Table.Td>
                  <Table.Td>{e.instance_name}</Table.Td>
                  <Table.Td>{actionLabel(e.action)}</Table.Td>
                  <Table.Td>
                    <Text size="sm" ff="monospace">
                      {e.target}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Badge color={resultColor[e.result] ?? 'gray'} variant="light">
                      {e.result}
                    </Badge>
                    {e.message && (
                      <Text size="xs" c="dimmed">
                        {e.message}
                      </Text>
                    )}
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
        {data.length === 0 && (
          <Text c="dimmed" ta="center" py="xl">
            No actions have been run yet.
          </Text>
        )}
      </Paper>
    </Stack>
  )
}
