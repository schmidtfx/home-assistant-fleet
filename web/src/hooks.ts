import { useQuery } from '@tanstack/react-query'
import { api, type ActionRecord, type Instance, type Role, type Task } from './api'

// Both queries are kept live by useFleetEvents; the fetches are a fallback.
export const useInstances = () => useQuery({ queryKey: ['instances'], queryFn: api.instances, staleTime: 60_000 })

export const useActions = () => useQuery({ queryKey: ['actions'], queryFn: api.actions, staleTime: 60_000 })

export const useMe = () => useQuery({ queryKey: ['me'], queryFn: api.me, retry: false, staleTime: Infinity })

const roleRank: Record<Role, number> = { viewer: 1, operator: 2, admin: 3 }

/**
 * Whether the current user has at least the given role. Only hides controls;
 * the server enforces roles on every request.
 */
export function useCan(min: Role): boolean {
  const { data } = useMe()
  return !!data && (roleRank[data.role] ?? 0) >= roleRank[min]
}

export const useSettings = () => useQuery({ queryKey: ['settings'], queryFn: api.settings, staleTime: 60_000 })

export const useAIStatus = () => useQuery({ queryKey: ['ai-status'], queryFn: api.aiStatus, staleTime: Infinity })

/** Cached AI reviews keyed by task id (kept live by ai_review events). */
export function useAIReviews() {
  const { data: status } = useAIStatus()
  return useQuery({ queryKey: ['ai-reviews'], queryFn: api.aiReviews, enabled: !!status?.enabled, staleTime: 60_000 })
}

export type FleetTask = Task & { instance_name: string; ai_enabled?: boolean }

export function fleetTasks(instances: Instance[] | undefined): FleetTask[] {
  return (instances ?? []).flatMap((i) =>
    i.tasks.map((t) => ({ ...t, instance_name: i.name, ai_enabled: i.ai_enabled })),
  )
}

/** Running action for a task, if any. */
export function runningAction(actions: ActionRecord[] | undefined, taskId: string): ActionRecord | undefined {
  return actions?.find((a) => a.task_id === taskId && a.status === 'running')
}
