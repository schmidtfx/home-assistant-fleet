import { useState } from 'react'
import { Alert, ColorSwatch, Group, Loader, Paper, SegmentedControl, Select, SimpleGrid, Stack, Table, Text, Title } from '@mantine/core'
import { BarChart } from '@mantine/charts'
import '@mantine/charts/styles.css'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { api, type HistorySummary } from '../api'
import { useInstances } from '../hooks'
import { formatDuration, kindLabels, relativeTime } from '../labels'
import './History.css'

const ranges = [
  { value: '7', label: '7 days' },
  { value: '30', label: '30 days' },
  { value: '90', label: '90 days' },
]

// Stacked bottom-up from most to least severe; the legend reads in the same order.
const severitySeries: Series[] = [
  { name: 'critical', label: 'Critical', color: 'var(--hf-sev-critical)' },
  { name: 'warning', label: 'Warning', color: 'var(--hf-sev-warning)' },
  { name: 'info', label: 'Info', color: 'var(--hf-sev-info)' },
]
const flowSeries: Series[] = [
  { name: 'opened', label: 'Opened', color: 'var(--hf-opened)' },
  { name: 'closed', label: 'Resolved', color: 'var(--hf-resolved)' },
]

type Series = { name: string; label: string; color: string }

/** Legend in series order (the chart's built-in legend reorders entries). */
function Legend({ series }: { series: Series[] }) {
  return (
    <Group gap="md" justify="center" mt="xs">
      {series.map((s) => (
        <Group key={s.name} gap={6}>
          <ColorSwatch color={s.color} size={10} withShadow={false} />
          <Text size="sm">{s.label}</Text>
        </Group>
      ))}
    </Group>
  )
}

const shortDate = (d: string) => new Date(d + 'T00:00:00').toLocaleDateString(undefined, { month: 'short', day: 'numeric' })

function Stat({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <Paper withBorder p="md" role="group" aria-label={label}>
      <Text size="xs" c="dimmed" tt="uppercase" fw={600}>
        {label}
      </Text>
      <Text fz={28} fw={600} lh={1.2}>
        {value}
      </Text>
      {hint && (
        <Text size="xs" c="dimmed">
          {hint}
        </Text>
      )}
    </Paper>
  )
}

function totals(h: HistorySummary) {
  const opened = h.days.reduce((n, d) => n + d.opened, 0)
  const resolved = h.days.reduce((n, d) => n + d.closed, 0)
  const openNow = h.days.at(-1)?.open ?? 0
  // Mean over all resolved tasks (each kind's mean weighted by its count). A fleet-wide median
  // would need the raw durations; the per-kind table shows medians.
  const resolvedKinds = h.kinds.filter((k) => k.resolved > 0)
  const mean = resolvedKinds.length
    ? resolvedKinds.reduce((s, k) => s + k.mean_seconds * k.resolved, 0) / resolvedKinds.reduce((s, k) => s + k.resolved, 0)
    : null
  return { opened, resolved, openNow, mean }
}

export default function History() {
  const [days, setDays] = useState('30')
  const [instance, setInstance] = useState<string | null>(null)
  const [view, setView] = useState('chart')
  const { data: instances } = useInstances()
  const q = useQuery({
    queryKey: ['history', days, instance],
    queryFn: () => api.history(Number(days), instance ?? undefined),
    staleTime: 60_000,
  })
  const instName = (id: string) => instances?.find((i) => i.id === id)?.name ?? id

  const rows = (q.data?.days ?? []).map((d) => ({
    date: shortDate(d.date),
    critical: d.open_by_severity.critical ?? 0,
    warning: d.open_by_severity.warning ?? 0,
    info: d.open_by_severity.info ?? 0,
    opened: d.opened,
    closed: d.closed,
  }))

  return (
    <Stack className="hf-history">
      <Group justify="space-between">
        <Title order={2}>History</Title>
        {/* Filters in one row above the charts; they apply to everything on the page. */}
        <Group>
          <Select
            aria-label="Instance"
            placeholder="All instances"
            clearable
            data={(instances ?? []).map((i) => ({ value: i.id, label: i.name }))}
            value={instance}
            onChange={setInstance}
            w={200}
          />
          <SegmentedControl data={ranges} value={days} onChange={setDays} />
        </Group>
      </Group>

      {q.isPending && <Loader />}
      {q.isError && <Alert color="red">{q.error.message}</Alert>}
      {q.data &&
        (() => {
          const t = totals(q.data)
          return (
            <>
              <SimpleGrid cols={{ base: 2, md: 4 }}>
                <Stat label="Open now" value={String(t.openNow)} />
                <Stat label="Opened" value={String(t.opened)} hint={`in the last ${days} days`} />
                <Stat label="Resolved" value={String(t.resolved)} hint={`in the last ${days} days`} />
                <Stat
                  label="Mean time to resolve"
                  value={t.mean === null ? '—' : formatDuration(t.mean)}
                  hint="tasks resolved in this period"
                />
              </SimpleGrid>

              <Paper withBorder p="md">
                <Group justify="space-between" mb="sm">
                  <Text fw={600}>Tasks per day</Text>
                  <SegmentedControl
                    size="xs"
                    data={[
                      { value: 'chart', label: 'Chart' },
                      { value: 'table', label: 'Table' },
                    ]}
                    value={view}
                    onChange={setView}
                  />
                </Group>
                {view === 'chart' ? (
                  <SimpleGrid cols={{ base: 1, lg: 2 }} spacing="xl">
                    <div>
                      <Text size="sm" c="dimmed" mb="xs">
                        Open at the end of each day, by severity
                      </Text>
                      <BarChart
                        h={240}
                        data={rows}
                        dataKey="date"
                        type="stacked"
                        series={severitySeries}
                        gridAxis="x"
                        strokeDasharray="0"
                        tickLine="none"
                        yAxisProps={{ allowDecimals: false }}
                        // Round only the top of the stack; a surface-colored stroke separates segments.
                        barProps={(s) => ({
                          radius: s.name === 'info' ? [4, 4, 0, 0] : 0,
                          stroke: 'var(--mantine-color-body)',
                          strokeWidth: 2,
                        })}
                        maxBarWidth={24}
                      />
                      <Legend series={severitySeries} />
                    </div>
                    <div>
                      <Text size="sm" c="dimmed" mb="xs">
                        Opened and resolved per day
                      </Text>
                      <BarChart
                        h={240}
                        data={rows}
                        dataKey="date"
                        series={flowSeries}
                        gridAxis="x"
                        strokeDasharray="0"
                        tickLine="none"
                        yAxisProps={{ allowDecimals: false }}
                        barProps={{ radius: [4, 4, 0, 0] }}
                        barChartProps={{ barGap: 2 }}
                        maxBarWidth={12}
                      />
                      <Legend series={flowSeries} />
                    </div>
                  </SimpleGrid>
                ) : (
                  <Table.ScrollContainer minWidth={500}>
                    <Table striped verticalSpacing={4}>
                      <Table.Thead>
                        <Table.Tr>
                          <Table.Th>Day</Table.Th>
                          <Table.Th ta="right">Open: critical</Table.Th>
                          <Table.Th ta="right">warning</Table.Th>
                          <Table.Th ta="right">info</Table.Th>
                          <Table.Th ta="right">Opened</Table.Th>
                          <Table.Th ta="right">Resolved</Table.Th>
                        </Table.Tr>
                      </Table.Thead>
                      <Table.Tbody>
                        {rows.map((r) => (
                          <Table.Tr key={r.date}>
                            <Table.Td>{r.date}</Table.Td>
                            <Table.Td ta="right">{r.critical}</Table.Td>
                            <Table.Td ta="right">{r.warning}</Table.Td>
                            <Table.Td ta="right">{r.info}</Table.Td>
                            <Table.Td ta="right">{r.opened}</Table.Td>
                            <Table.Td ta="right">{r.closed}</Table.Td>
                          </Table.Tr>
                        ))}
                      </Table.Tbody>
                    </Table>
                  </Table.ScrollContainer>
                )}
              </Paper>

              <SimpleGrid cols={{ base: 1, lg: 2 }}>
                <Paper withBorder p="md">
                  <Text fw={600} mb="sm">
                    Time to resolve by kind
                  </Text>
                  <Table verticalSpacing={4}>
                    <Table.Thead>
                      <Table.Tr>
                        <Table.Th>Kind</Table.Th>
                        <Table.Th ta="right">Opened</Table.Th>
                        <Table.Th ta="right">Resolved</Table.Th>
                        <Table.Th ta="right">Median</Table.Th>
                        <Table.Th ta="right">Mean</Table.Th>
                      </Table.Tr>
                    </Table.Thead>
                    <Table.Tbody>
                      {q.data.kinds.map((k) => (
                        <Table.Tr key={k.kind}>
                          <Table.Td>{kindLabels[k.kind] ?? k.kind}</Table.Td>
                          <Table.Td ta="right">{k.opened}</Table.Td>
                          <Table.Td ta="right">{k.resolved}</Table.Td>
                          <Table.Td ta="right">{k.resolved ? formatDuration(k.median_seconds) : '—'}</Table.Td>
                          <Table.Td ta="right">{k.resolved ? formatDuration(k.mean_seconds) : '—'}</Table.Td>
                        </Table.Tr>
                      ))}
                    </Table.Tbody>
                  </Table>
                  {q.data.kinds.length === 0 && (
                    <Text size="sm" c="dimmed" ta="center" py="md">
                      No tasks in this period.
                    </Text>
                  )}
                </Paper>

                <Paper withBorder p="md">
                  <Text fw={600} mb="sm">
                    Recurring tasks
                  </Text>
                  <Table verticalSpacing={4}>
                    <Table.Thead>
                      <Table.Tr>
                        <Table.Th>Task</Table.Th>
                        <Table.Th>Instance</Table.Th>
                        <Table.Th ta="right">Times</Table.Th>
                        <Table.Th>Last</Table.Th>
                      </Table.Tr>
                    </Table.Thead>
                    <Table.Tbody>
                      {q.data.recurring.map((r) => (
                        <Table.Tr key={r.instance_id + r.task_id}>
                          <Table.Td>
                            <Text size="sm">{r.title}</Text>
                            <Text size="xs" c="dimmed">
                              {kindLabels[r.kind] ?? r.kind}
                            </Text>
                          </Table.Td>
                          <Table.Td>
                            <Link to={`/instances/${r.instance_id}`}>{instName(r.instance_id)}</Link>
                          </Table.Td>
                          <Table.Td ta="right">{r.count}</Table.Td>
                          <Table.Td>{relativeTime(r.last_opened)}</Table.Td>
                        </Table.Tr>
                      ))}
                    </Table.Tbody>
                  </Table>
                  {q.data.recurring.length === 0 && (
                    <Text size="sm" c="dimmed" ta="center" py="md">
                      Nothing came back more than once.
                    </Text>
                  )}
                </Paper>
              </SimpleGrid>
            </>
          )
        })()}
    </Stack>
  )
}
