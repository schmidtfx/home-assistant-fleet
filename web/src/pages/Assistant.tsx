import { useRef, useState } from 'react'
import { Alert, Badge, Button, Card, Group, Paper, ScrollArea, Stack, Text, Textarea, Title, Typography } from '@mantine/core'
import { IconSend, IconSparkles } from '@tabler/icons-react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { api, type ChatEvent, type Proposal } from '../api'
import ActionButton from '../components/ActionButton'
import { runningAction, useActions, useAIStatus } from '../hooks'
import { actionLabels } from '../labels'

interface Turn {
  role: 'user' | 'assistant'
  text: string
  tools: { name: string; status: string }[]
  proposals: Proposal[]
  error?: string
}

const toolLabels: Record<string, string> = {
  list_instances: 'Listing instances',
  list_tasks: 'Looking up tasks',
  get_instance: 'Reading instance',
  get_update_review: 'Reviewing release notes',
  explain_task: 'Explaining task',
  recent_actions: 'Reading audit log',
  get_history: 'Reading task history',
  list_rollouts: 'Reading rollouts',
  get_drift: 'Comparing instances',
  propose_action: 'Preparing proposal',
}

function ProposalCard({ p }: { p: Proposal }) {
  const { data: actions } = useActions()
  return (
    <Card withBorder padding="sm">
      <Text size="sm">
        Proposed: <b>{actionLabels[p.action]}</b> on <b>{p.instance_name}</b> — {p.task.title}
      </Text>
      {p.reason && (
        <Text size="xs" c="dimmed">
          {p.reason}
        </Text>
      )}
      <Group mt="xs">
        {/* The regular confirmation dialog and actions endpoint; nothing runs without the user. */}
        <ActionButton
          aiEnabled
          task={p.task}
          action={p.action}
          instanceName={p.instance_name}
          running={!!runningAction(actions, p.task.id)}
        />
      </Group>
    </Card>
  )
}

export default function Assistant() {
  const { data: status } = useAIStatus()
  const [turns, setTurns] = useState<Turn[]>([])
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const conversation = useRef<string | undefined>(undefined)
  const viewport = useRef<HTMLDivElement>(null)

  const update = (fn: (t: Turn) => Turn) =>
    setTurns((ts) => [...ts.slice(0, -1), fn(ts[ts.length - 1])])

  const send = async () => {
    const message = input.trim()
    if (!message || busy) return
    setInput('')
    setBusy(true)
    setTurns((ts) => [
      ...ts,
      { role: 'user', text: message, tools: [], proposals: [] },
      { role: 'assistant', text: '', tools: [], proposals: [] },
    ])
    const onEvent = (ev: ChatEvent) => {
      switch (ev.type) {
        case 'conversation':
          conversation.current = ev.conversation_id
          break
        case 'text':
          update((t) => ({ ...t, text: t.text + (ev.text ?? '') }))
          break
        case 'tool':
          update((t) => {
            const tools = [...t.tools]
            const i = tools.findLastIndex((x) => x.name === ev.tool && x.status === 'running')
            if (ev.status === 'running' || i < 0) tools.push({ name: ev.tool!, status: ev.status! })
            else tools[i] = { name: ev.tool!, status: ev.status! }
            return { ...t, tools }
          })
          break
        case 'proposal':
          update((t) => ({ ...t, proposals: [...t.proposals, ev.proposal!] }))
          break
        case 'error':
          update((t) => ({ ...t, error: ev.text }))
          break
      }
      viewport.current?.scrollTo?.({ top: viewport.current.scrollHeight })
    }
    try {
      await api.chat(conversation.current, message, onEvent)
    } catch (err) {
      update((t) => ({ ...t, error: (err as Error).message }))
    } finally {
      setBusy(false)
    }
  }

  if (status && !status.enabled) {
    return <Alert>AI features are not configured on this server.</Alert>
  }

  return (
    <Stack h="calc(100vh - 90px)">
      <Group justify="space-between">
        <Group gap="xs">
          <IconSparkles size={20} />
          <Title order={2}>Assistant</Title>
        </Group>
        <Group gap="xs">
          <Text size="xs" c="dimmed">
            {status?.model} · sees only instances with AI enabled · cannot run actions
          </Text>
          <Button
            size="xs"
            variant="subtle"
            disabled={busy}
            onClick={() => {
              setTurns([])
              conversation.current = undefined
            }}
          >
            New chat
          </Button>
        </Group>
      </Group>
      <Paper withBorder p="md" style={{ flex: 1, minHeight: 0 }}>
        <ScrollArea h="100%" viewportRef={viewport}>
          <Stack>
            {turns.length === 0 && (
              <Text c="dimmed" size="sm">
                Ask about your fleet, e.g. “Which instances still need the Mosquitto update?” or “What should I do before
                updating Core on Cabin?”
              </Text>
            )}
            {turns.map((t, i) =>
              t.role === 'user' ? (
                <Paper key={i} p="sm" bg="var(--mantine-color-default-hover)" ml="20%">
                  <Text size="sm">{t.text}</Text>
                </Paper>
              ) : (
                <Stack key={i} gap="xs" mr="10%">
                  {t.tools.length > 0 && (
                    <Group gap={4}>
                      {t.tools.map((tool, j) => (
                        <Badge
                          key={j}
                          size="xs"
                          variant="light"
                          color={tool.status === 'error' ? 'red' : tool.status === 'running' ? 'gray' : 'blue'}
                        >
                          {toolLabels[tool.name] ?? tool.name}
                          {tool.status === 'running' ? '…' : ''}
                        </Badge>
                      ))}
                    </Group>
                  )}
                  {t.text && (
                    <Typography>
                      <Markdown remarkPlugins={[remarkGfm]}>{t.text}</Markdown>
                    </Typography>
                  )}
                  {t.proposals.map((p) => (
                    <ProposalCard key={p.id} p={p} />
                  ))}
                  {t.error && <Alert color="red">{t.error}</Alert>}
                </Stack>
              ),
            )}
          </Stack>
        </ScrollArea>
      </Paper>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          send()
        }}
      >
        <Group align="flex-end">
          <Textarea
            style={{ flex: 1 }}
            autosize
            minRows={1}
            maxRows={5}
            placeholder="Ask about your fleet…"
            value={input}
            maxLength={4000}
            onChange={(e) => setInput(e.currentTarget.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault()
                send()
              }
            }}
          />
          <Button type="submit" loading={busy} leftSection={<IconSend size={16} />}>
            Send
          </Button>
        </Group>
      </form>
    </Stack>
  )
}
