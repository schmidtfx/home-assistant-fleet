import { useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { notifications } from '@mantine/notifications'
import type { ActionRecord, Instance, Review, Rollout } from './api'
import { actionLabel } from './labels'

interface Snapshot {
  instances: Instance[]
  actions: ActionRecord[]
}

interface FleetEvent {
  type: string
  id?: string
  instance?: Instance
  action?: ActionRecord
  data?: unknown
}

function upsertInstance(list: Instance[] | undefined, inst: Instance): Instance[] {
  const out = (list ?? []).filter((i) => i.id !== inst.id)
  out.push(inst)
  return out.sort((a, b) => a.name.localeCompare(b.name))
}

/**
 * Subscribes to /api/events and keeps the ['instances'] and ['actions'] query
 * caches live. Returns whether the stream is currently connected.
 */
export function useFleetEvents(): boolean {
  const qc = useQueryClient()
  const [connected, setConnected] = useState(false)

  useEffect(() => {
    const es = new EventSource('/api/events')
    es.onopen = () => setConnected(true)
    es.onerror = () => setConnected(false) // EventSource reconnects automatically

    es.addEventListener('snapshot', (e) => {
      const snap: Snapshot = JSON.parse((e as MessageEvent).data)
      qc.setQueryData(['instances'], snap.instances)
      qc.setQueryData(['actions'], snap.actions)
    })
    es.addEventListener('instance', (e) => {
      const ev: FleetEvent = JSON.parse((e as MessageEvent).data)
      if (ev.instance) qc.setQueryData<Instance[]>(['instances'], (old) => upsertInstance(old, ev.instance!))
      qc.invalidateQueries({ queryKey: ['drift'] }) // only refetches while the drift page is open
    })
    es.addEventListener('instance_removed', (e) => {
      const ev: FleetEvent = JSON.parse((e as MessageEvent).data)
      qc.setQueryData<Instance[]>(['instances'], (old) => (old ?? []).filter((i) => i.id !== ev.id))
      qc.invalidateQueries({ queryKey: ['drift'] })
    })
    es.addEventListener('action', (e) => {
      const ev: FleetEvent = JSON.parse((e as MessageEvent).data)
      const rec = ev.action
      if (!rec) return
      qc.setQueryData<ActionRecord[]>(['actions'], (old) => [rec, ...(old ?? []).filter((a) => a.id !== rec.id)])
      if (rec.status !== 'running') {
        notifications.show({
          color: rec.status === 'succeeded' ? 'green' : 'red',
          title: `${actionLabel(rec.action)} ${rec.status}`,
          message: `${rec.target}${rec.message ? ` — ${rec.message}` : ''}`,
        })
        qc.invalidateQueries({ queryKey: ['audit'] })
      }
    })
    es.addEventListener('rollout', (e) => {
      const r = (JSON.parse((e as MessageEvent).data) as FleetEvent).data as Rollout | undefined
      if (!r) return
      qc.setQueryData<Rollout[]>(['rollouts'], (old) =>
        old ? [r, ...old.filter((x) => x.id !== r.id)].sort((a, b) => b.created_at.localeCompare(a.created_at)) : old,
      )
    })
    es.addEventListener('ai_review', (e) => {
      const r = (JSON.parse((e as MessageEvent).data) as FleetEvent).data as Review | undefined
      if (r) qc.setQueryData<Record<string, Review>>(['ai-reviews'], (old) => ({ ...(old ?? {}), [r.task_id]: r }))
    })
    return () => es.close()
  }, [qc])

  return connected
}
