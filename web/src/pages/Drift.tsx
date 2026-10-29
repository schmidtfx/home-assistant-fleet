import { Fragment, useState } from 'react'
import { Alert, Badge, Group, Loader, Paper, SegmentedControl, Stack, Switch, Table, Text, Title, Tooltip } from '@mantine/core'
import { IconAlertTriangle } from '@tabler/icons-react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { api, type DriftInstance, type DriftRow } from '../api'

const categories = [
  { value: 'all', label: 'All' },
  { value: 'version', label: 'Versions' },
  { value: 'addon', label: 'Add-ons' },
  { value: 'integration', label: 'Integrations' },
]

const categoryLabels: Record<DriftRow['category'], string> = {
  version: 'Versions',
  addon: 'Add-ons',
  integration: 'Integrations',
}

function Cell({ row, inst }: { row: DriftRow; inst: DriftInstance }) {
  if (!inst.known) {
    return (
      <Text size="sm" c="dimmed">
        ?
      </Text>
    )
  }
  const value = row.values[inst.id] ?? ''
  const shown = value === '' ? '—' : value === 'installed' ? '✓' : value
  // Without a clear majority (e.g. two instances that differ) no cell is singled out; the row is marked instead.
  if (!row.drift || !row.has_majority || value === row.majority) {
    return (
      <Text size="sm" c={value === '' ? 'dimmed' : undefined}>
        {shown}
      </Text>
    )
  }
  const most = row.majority === '' ? 'not installed' : row.majority === 'installed' ? 'installed' : row.majority
  return (
    <Tooltip label={`Differs from most instances (${most})`}>
      <Badge color="orange" variant="light" radius="sm" leftSection={<IconAlertTriangle size={12} />} tt="none">
        {value === '' ? 'missing' : shown}
      </Badge>
    </Tooltip>
  )
}

export default function Drift() {
  const [category, setCategory] = useState('all')
  const [onlyDrift, setOnlyDrift] = useState(true)
  const q = useQuery({ queryKey: ['drift'], queryFn: api.drift, staleTime: 30_000 })

  if (q.isPending) return <Loader />
  if (q.isError) return <Alert color="red">{q.error.message}</Alert>

  const { instances, rows } = q.data
  const visible = rows.filter((r) => (category === 'all' || r.category === category) && (!onlyDrift || r.drift))
  const driftCount = rows.filter((r) => r.drift).length

  return (
    <Stack>
      <Group justify="space-between">
        <div>
          <Title order={2}>Drift</Title>
          <Text size="sm" c="dimmed">
            Versions, add-ons and integrations compared across instances. {driftCount} item
            {driftCount === 1 ? '' : 's'} differ.
          </Text>
        </div>
        <Group>
          <SegmentedControl data={categories} value={category} onChange={setCategory} />
          <Switch label="Only differences" checked={onlyDrift} onChange={(e) => setOnlyDrift(e.currentTarget.checked)} />
        </Group>
      </Group>

      {instances.length < 2 ? (
        <Text c="dimmed">Add at least two instances to compare them.</Text>
      ) : (
        <Paper withBorder>
          <Table.ScrollContainer minWidth={200 + instances.length * 140}>
            <Table verticalSpacing={6} stickyHeader>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Item</Table.Th>
                  {instances.map((i) => (
                    <Table.Th key={i.id}>
                      <Link to={`/instances/${i.id}`}>{i.name}</Link>
                      {!i.known && (
                        <Text size="xs" c="dimmed">
                          not synced yet
                        </Text>
                      )}
                    </Table.Th>
                  ))}
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {visible.map((r, idx) => (
                  <Fragment key={r.category + r.key}>
                    {(idx === 0 || visible[idx - 1].category !== r.category) && (
                      <Table.Tr key={`h-${r.category}`}>
                        <Table.Td colSpan={instances.length + 1} bg="var(--mantine-color-default-hover)">
                          <Text size="xs" fw={600} tt="uppercase" c="dimmed">
                            {categoryLabels[r.category]}
                          </Text>
                        </Table.Td>
                      </Table.Tr>
                    )}
                    <Table.Tr>
                      <Table.Td>
                        <Group gap={6} wrap="nowrap">
                          <Text size="sm">{r.label}</Text>
                          {r.drift && !r.has_majority && (
                            <Tooltip label="Instances differ and no value is shared by most of them">
                              <Badge color="orange" variant="light" radius="sm" leftSection={<IconAlertTriangle size={12} />} tt="none">
                                differs
                              </Badge>
                            </Tooltip>
                          )}
                        </Group>
                        {r.category === 'addon' && (
                          <Text size="xs" c="dimmed" ff="monospace">
                            {r.key}
                          </Text>
                        )}
                      </Table.Td>
                      {instances.map((i) => (
                        <Table.Td key={i.id}>
                          <Cell row={r} inst={i} />
                        </Table.Td>
                      ))}
                    </Table.Tr>
                  </Fragment>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
          {visible.length === 0 && (
            <Text c="dimmed" ta="center" py="xl">
              {onlyDrift ? 'No differences. All instances match.' : 'Nothing to compare yet.'}
            </Text>
          )}
        </Paper>
      )}
    </Stack>
  )
}
