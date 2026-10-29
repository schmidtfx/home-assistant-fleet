import { useMemo, useState } from 'react'
import { Alert, Group, Loader, MultiSelect, Paper, SegmentedControl, Stack, Switch, TextInput, Title } from '@mantine/core'
import { IconSearch } from '@tabler/icons-react'
import { fleetTasks, useInstances } from '../hooks'
import { isAcked, kindLabels } from '../labels'
import TaskTable from '../components/TaskTable'

export default function Tasks() {
  const { data: instances, isPending, isError, error } = useInstances()
  const [severity, setSeverity] = useState('all')
  const [kinds, setKinds] = useState<string[]>([])
  const [instanceIds, setInstanceIds] = useState<string[]>([])
  const [search, setSearch] = useState('')
  const [showAcked, setShowAcked] = useState(false)

  const all = useMemo(() => fleetTasks(instances), [instances])
  const ackedCount = all.filter(isAcked).length
  const filtered = all.filter(
    (t) =>
      (showAcked || !isAcked(t)) &&
      (severity === 'all' || t.severity === severity) &&
      (kinds.length === 0 || kinds.includes(t.kind)) &&
      (instanceIds.length === 0 || instanceIds.includes(t.instance_id)) &&
      (!search || `${t.title} ${t.detail ?? ''}`.toLowerCase().includes(search.toLowerCase())),
  )

  if (isPending) return <Loader />
  if (isError) return <Alert color="red">{error.message}</Alert>

  return (
    <Stack>
      <Title order={2}>Tasks</Title>
      <Group align="flex-end">
        <SegmentedControl
          value={severity}
          onChange={setSeverity}
          data={[
            { value: 'all', label: 'All' },
            { value: 'critical', label: 'Critical' },
            { value: 'warning', label: 'Warning' },
            { value: 'info', label: 'Info' },
          ]}
        />
        <MultiSelect
          placeholder="Kind"
          data={Object.entries(kindLabels).map(([value, label]) => ({ value, label }))}
          value={kinds}
          onChange={setKinds}
          clearable
          w={240}
        />
        <MultiSelect
          placeholder="Instance"
          data={instances.map((i) => ({ value: i.id, label: i.name }))}
          value={instanceIds}
          onChange={setInstanceIds}
          clearable
          w={240}
        />
        <TextInput
          placeholder="Search"
          leftSection={<IconSearch size={14} />}
          value={search}
          onChange={(e) => setSearch(e.currentTarget.value)}
        />
        <Switch
          label={`Show acknowledged (${ackedCount})`}
          checked={showAcked}
          onChange={(e) => setShowAcked(e.currentTarget.checked)}
          mb={8}
        />
      </Group>
      <Paper withBorder>
        <TaskTable tasks={filtered} />
      </Paper>
    </Stack>
  )
}
