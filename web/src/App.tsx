import { AppShell, Badge, Burger, Button, Center, Group, Loader, NavLink, Text, Title, Tooltip } from '@mantine/core'
import { useDisclosure } from '@mantine/hooks'
import { IconArrowsDiff, IconChartBar, IconHistory, IconRocket, IconLayoutDashboard, IconListCheck, IconLogout, IconServer, IconSettings, IconSparkles } from '@tabler/icons-react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { lazy, Suspense } from 'react'
import { Link, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { api, type Role } from './api'
import { useFleetEvents } from './events'
import { useAIStatus, useCan, useMe } from './hooks'
import Dashboard from './pages/Dashboard'
import Login from './pages/Login'

// Pages other than the landing page load on demand so heavy dependencies
// (recharts, react-markdown) stay out of the initial bundle.
const Assistant = lazy(() => import('./pages/Assistant'))
const Audit = lazy(() => import('./pages/Audit'))
const Drift = lazy(() => import('./pages/Drift'))
const History = lazy(() => import('./pages/History'))
const InstanceDetail = lazy(() => import('./pages/InstanceDetail'))
const Instances = lazy(() => import('./pages/Instances'))
const Rollouts = lazy(() => import('./pages/Rollouts'))
const Settings = lazy(() => import('./pages/Settings'))
const Tasks = lazy(() => import('./pages/Tasks'))

export default function App() {
  const me = useMe()
  if (me.isPending) {
    return (
      <Center h="100vh">
        <Loader />
      </Center>
    )
  }
  if (me.isError) return <Login />
  return <Shell username={me.data.username} role={me.data.role} />
}

const nav = [
  { to: '/', label: 'Fleet', icon: IconLayoutDashboard },
  { to: '/tasks', label: 'Tasks', icon: IconListCheck },
  { to: '/rollouts', label: 'Rollouts', icon: IconRocket },
  { to: '/history', label: 'History', icon: IconChartBar },
  { to: '/drift', label: 'Drift', icon: IconArrowsDiff },
  { to: '/instances', label: 'Instances', icon: IconServer },
  { to: '/audit', label: 'Audit log', icon: IconHistory },
  { to: '/settings', label: 'Settings', icon: IconSettings },
]

function Shell({ username, role }: { username: string; role: Role }) {
  const [opened, { toggle, close }] = useDisclosure()
  const location = useLocation()
  const qc = useQueryClient()
  const live = useFleetEvents()
  const { data: ai } = useAIStatus()
  const canOperate = useCan('operator') // the assistant can call the model, which needs operator
  const links = ai?.enabled && canOperate ? [...nav.slice(0, 2), { to: '/assistant', label: 'Assistant', icon: IconSparkles }, ...nav.slice(2)] : nav
  const logout = useMutation({
    mutationFn: api.logout,
    onSettled: () => {
      qc.clear()
      qc.invalidateQueries({ queryKey: ['me'] })
    },
  })

  return (
    <AppShell header={{ height: 56 }} navbar={{ width: 220, breakpoint: 'sm', collapsed: { mobile: !opened } }} padding="md">
      <AppShell.Header>
        <Group h="100%" px="md" justify="space-between">
          <Group>
            <Burger opened={opened} onClick={toggle} hiddenFrom="sm" size="sm" />
            <Title order={4}>HA Fleet</Title>
            <Tooltip label={live ? 'Receiving live updates' : 'Live updates disconnected, reconnecting…'}>
              <Badge size="sm" variant="dot" color={live ? 'green' : 'gray'}>
                {live ? 'live' : 'offline'}
              </Badge>
            </Tooltip>
          </Group>
          <Group gap="xs">
            <Text size="sm" c="dimmed">
              {username}
            </Text>
            {role !== 'admin' && (
              <Badge size="sm" variant="light" color="gray">
                {role}
              </Badge>
            )}
            <Button
              variant="subtle"
              size="xs"
              leftSection={<IconLogout size={14} />}
              onClick={() => logout.mutate()}
              loading={logout.isPending}
            >
              Log out
            </Button>
          </Group>
        </Group>
      </AppShell.Header>
      <AppShell.Navbar p="xs">
        {links.map((n) => (
          <NavLink
            key={n.to}
            component={Link}
            to={n.to}
            label={n.label}
            leftSection={<n.icon size={18} />}
            active={n.to === '/' ? location.pathname === '/' : location.pathname.startsWith(n.to)}
            onClick={close}
          />
        ))}
      </AppShell.Navbar>
      <AppShell.Main>
        <Suspense
          fallback={
            <Center py="xl">
              <Loader />
            </Center>
          }
        >
          <Routes>
            <Route path="/" element={<Dashboard />} />
            <Route path="/tasks" element={<Tasks />} />
            <Route path="/instances" element={<Instances />} />
            <Route path="/instances/:id" element={<InstanceDetail />} />
            <Route path="/rollouts" element={<Rollouts />} />
            <Route path="/history" element={<History />} />
            <Route path="/drift" element={<Drift />} />
            <Route path="/audit" element={<Audit />} />
            <Route path="/settings" element={<Settings />} />
            <Route path="/assistant" element={<Assistant />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </Suspense>
      </AppShell.Main>
    </AppShell>
  )
}
