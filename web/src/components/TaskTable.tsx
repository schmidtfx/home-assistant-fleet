import { useState } from 'react'
import { Anchor, Badge, Group, List, Table, Text, Tooltip } from '@mantine/core'
import { IconFileText, IconRocket, IconSparkles } from '@tabler/icons-react'
import { Link } from 'react-router-dom'
import type { Task } from '../api'
import { runningAction, useActions, useAIStatus, useCan, useInstances, type FleetTask } from '../hooks'
import { isAcked, kindLabels, relativeTime, severityColor } from '../labels'
import AckButton from './AckButton'
import ActionButton from './ActionButton'
import ReleaseNotesDrawer from './ReleaseNotesDrawer'
import ExplainDrawer from './ExplainDrawer'
import RolloutModal, { rolloutCandidates } from './RolloutModal'
import { RiskBadge } from './AIReview'

interface Props {
  tasks: FleetTask[]
  showInstance?: boolean
}

/** Entity IDs or log lines attached to health tasks (kept out of titles, which go to AI features). */
function SourceList({ task }: { task: Task }) {
  const [open, setOpen] = useState(false)
  const entities = task.source?.entities as string[] | undefined
  const messages = task.source?.messages as string[] | undefined
  const items = entities ?? messages
  if (!items?.length) return null
  const noun = entities ? (items.length === 1 ? 'entity' : 'entities') : items.length === 1 ? 'message' : 'messages'
  return (
    <>
      <Anchor component="button" size="xs" onClick={() => setOpen((o) => !o)} display="block" mt={2}>
        {open ? 'Hide' : 'Show'} {items.length} {noun}
      </Anchor>
      {open && (
        <List size="xs" spacing={0} mt={2} ff={entities ? 'monospace' : undefined}>
          {items.map((s, i) => (
            <List.Item key={i}>{s}</List.Item>
          ))}
        </List>
      )}
    </>
  )
}

export default function TaskTable({ tasks, showInstance = true }: Props) {
  const { data: actions } = useActions()
  const [notesFor, setNotesFor] = useState<FleetTask | null>(null)
  const [explainFor, setExplainFor] = useState<Task | null>(null)
  const [rolloutFor, setRolloutFor] = useState<Task | null>(null)
  const { data: instances } = useInstances()
  const { data: ai } = useAIStatus()
  const canOperate = useCan('operator')

  if (tasks.length === 0) {
    return (
      <Text c="dimmed" ta="center" py="xl">
        Nothing to do. 🎉
      </Text>
    )
  }

  return (
    <>
      <Table.ScrollContainer minWidth={800}>
        <Table verticalSpacing="sm" highlightOnHover>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Severity</Table.Th>
              {showInstance && <Table.Th>Instance</Table.Th>}
              <Table.Th>Kind</Table.Th>
              <Table.Th>Task</Table.Th>
              <Table.Th>Seen</Table.Th>
              <Table.Th>Actions</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {tasks.map((t) => {
              const running = runningAction(actions, t.id)
              const inProgress = t.source?.in_progress === true
              const learnMore = t.source?.learn_more_url as string | undefined
              return (
                <Table.Tr key={t.id} style={isAcked(t) ? { opacity: 0.55 } : undefined}>
                  <Table.Td>
                    <Badge color={severityColor[t.severity]} variant="light">
                      {t.severity}
                    </Badge>
                  </Table.Td>
                  {showInstance && (
                    <Table.Td>
                      <Anchor component={Link} to={`/instances/${t.instance_id}`} size="sm">
                        {t.instance_name}
                      </Anchor>
                    </Table.Td>
                  )}
                  <Table.Td>
                    <Text size="sm">{kindLabels[t.kind] ?? t.kind}</Text>
                  </Table.Td>
                  <Table.Td>
                    <Text size="sm" fw={500}>
                      {t.title}
                    </Text>
                    {t.detail && (
                      <Text size="xs" c="dimmed">
                        {t.detail}
                      </Text>
                    )}
                    <SourceList task={t} />
                    {isAcked(t) && (
                      <Badge size="xs" color="gray" variant="outline" mt={4} mr={4}>
                        acknowledged{t.ack?.note ? `: ${t.ack.note}` : ''}
                      </Badge>
                    )}
                    {ai?.enabled && t.ai_enabled && t.kind === 'update' && <RiskBadge taskId={t.id} />}
                    {inProgress && (
                      <Badge size="xs" color="grape" mt={4}>
                        installing{typeof t.source?.progress === 'number' ? ` ${t.source.progress}%` : '…'}
                      </Badge>
                    )}
                  </Table.Td>
                  <Table.Td>
                    <Tooltip label={new Date(t.first_seen).toLocaleString()}>
                      <Text size="xs" c="dimmed">
                        {relativeTime(t.first_seen)}
                      </Text>
                    </Tooltip>
                  </Table.Td>
                  <Table.Td>
                    <Group gap={6} wrap="nowrap">
                      {t.kind === 'update' && (
                        <Tooltip label="Release notes">
                          <Anchor component="button" onClick={() => setNotesFor(t)} aria-label="Release notes">
                            <IconFileText size={18} />
                          </Anchor>
                        </Tooltip>
                      )}
                      {canOperate && ai?.enabled && t.ai_enabled && t.kind !== 'update' && (
                        <Tooltip label="Explain with AI">
                          <Anchor component="button" onClick={() => setExplainFor(t)} aria-label="Explain">
                            <IconSparkles size={18} />
                          </Anchor>
                        </Tooltip>
                      )}
                      {canOperate && t.kind === 'update' && rolloutCandidates(instances, t).length > 1 && (
                        <Tooltip label="Roll out to all instances with this update">
                          <Anchor component="button" onClick={() => setRolloutFor(t)} aria-label="Roll out">
                            <IconRocket size={18} />
                          </Anchor>
                        </Tooltip>
                      )}
                      {learnMore && (
                        <Anchor href={learnMore} target="_blank" rel="noopener noreferrer" size="xs">
                          Learn more
                        </Anchor>
                      )}
                      {t.actions.map((a) => (
                        <ActionButton
                          key={a}
                          task={t}
                          action={a}
                          instanceName={t.instance_name}
                          running={running?.action === a}
                          aiEnabled={t.ai_enabled}
                          disabled={!!running || inProgress}
                        />
                      ))}
                      <AckButton task={t} instanceName={t.instance_name} />
                    </Group>
                  </Table.Td>
                </Table.Tr>
              )
            })}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
      <ReleaseNotesDrawer task={notesFor} aiEnabled={!!ai?.enabled && !!notesFor?.ai_enabled} onClose={() => setNotesFor(null)} />
      <ExplainDrawer task={explainFor} onClose={() => setExplainFor(null)} />
      {rolloutFor && <RolloutModal task={rolloutFor} opened onClose={() => setRolloutFor(null)} />}
    </>
  )
}
