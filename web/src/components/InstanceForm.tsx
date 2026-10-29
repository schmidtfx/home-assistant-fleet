import { useState } from 'react'
import {
  Alert,
  Button,
  Collapse,
  Group,
  PasswordInput,
  Stack,
  Switch,
  Text,
  TextInput,
  Textarea,
  UnstyledButton,
} from '@mantine/core'
import { useMutation } from '@tanstack/react-query'
import { api, type CheckSettings, type Instance, type InstanceInput, type TestResult } from '../api'
import { useAIStatus } from '../hooks'
import { CheckOverrides } from './CheckSettingsFields'

interface Props {
  instance?: Instance
  onSaved: () => void
  onCancel: () => void
}

/** Create/edit form. The token is write-only: it is never loaded back from the server. */
export default function InstanceForm({ instance, onSaved, onCancel }: Props) {
  const editing = !!instance
  const [name, setName] = useState(instance?.name ?? '')
  const [url, setUrl] = useState(instance?.url ?? 'https://')
  const [token, setToken] = useState('')
  const [fingerprint, setFingerprint] = useState(instance?.cert_fingerprint ?? '')
  const [caPem, setCaPem] = useState('')
  const [clearCa, setClearCa] = useState(false)
  const [insecure, setInsecure] = useState(instance?.allow_insecure_http ?? false)
  const [enabled, setEnabled] = useState(instance?.enabled ?? true)
  const [aiEnabled, setAiEnabled] = useState(instance?.ai_enabled ?? false)
  const [maintWindow, setMaintWindow] = useState(instance?.maint_window ?? '')
  const [publishStatus, setPublishStatus] = useState(instance?.publish_status ?? false)
  const { data: ai } = useAIStatus()
  const [advanced, setAdvanced] = useState(!!instance?.cert_fingerprint || !!instance?.has_ca_pem)
  const [checkSettings, setCheckSettings] = useState<CheckSettings>(instance?.settings ?? {})
  const hasOverrides =
    Object.keys(checkSettings.checks ?? {}).length > 0 ||
    checkSettings.disk_free_min !== undefined ||
    checkSettings.battery_min !== undefined ||
    checkSettings.backup_max_age_hours !== undefined
  const [overridesOpen, setOverridesOpen] = useState(hasOverrides)

  const body = (): InstanceInput => {
    const b: InstanceInput = {
      name,
      url,
      cert_fingerprint: fingerprint,
      allow_insecure_http: insecure,
      enabled,
      ai_enabled: aiEnabled,
      maint_window: maintWindow,
      publish_status: publishStatus,
      settings: checkSettings,
    }
    if (token) b.token = token
    if (caPem || clearCa || !editing) b.ca_pem = caPem
    return b
  }

  const test = useMutation<TestResult>({
    mutationFn: () => api.testInstance({ ...body(), instance_id: instance?.id }),
  })
  const save = useMutation({
    mutationFn: () => (editing ? api.updateInstance(instance.id, body()) : api.createInstance(body())),
    onSuccess: onSaved,
  })

  const isHttp = url.trim().toLowerCase().startsWith('http://')

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        save.mutate()
      }}
    >
      <Stack>
        <TextInput label="Name" value={name} onChange={(e) => setName(e.currentTarget.value)} required maxLength={100} />
        <TextInput
          label="URL"
          description="Base URL of the Home Assistant instance, e.g. https://ha.example.com:8123"
          value={url}
          onChange={(e) => setUrl(e.currentTarget.value)}
          required
        />
        <PasswordInput
          label="Long-lived access token"
          description={
            editing
              ? 'Leave empty to keep the stored token.'
              : 'Create one in Home Assistant under Profile → Security. Use an admin user to enable actions.'
          }
          value={token}
          onChange={(e) => setToken(e.currentTarget.value)}
          required={!editing}
          autoComplete="off"
        />
        <TextInput
          label="Maintenance window"
          description="Rollouts only install updates on this instance during this time of day (server time), e.g. 02:00-05:00. Leave empty for any time."
          placeholder="02:00-05:00"
          value={maintWindow}
          onChange={(e) => setMaintWindow(e.currentTarget.value)}
        />
        <Switch label="Enabled" checked={enabled} onChange={(e) => setEnabled(e.currentTarget.checked)} />
        {ai?.enabled && (
          <Switch
            label="Enable AI features"
            description={`Sends task details, versions, integration names and release notes to ${ai.provider === 'ollama' ? 'your local Ollama model' : 'the Anthropic API'}. URLs, IPs, the location name and tokens are removed first.`}
            checked={aiEnabled}
            onChange={(e) => setAiEnabled(e.currentTarget.checked)}
          />
        )}
        <Switch
          label="Publish status to Home Assistant"
          description={
            'Sends this instance\'s open tasks to it as a hafleet_status event. Needs an admin token and the hafleet integration from HACS.' +
            (instance?.status.user_id ? ` Select the user with ID ${instance.status.user_id} when setting up the integration.` : '')
          }
          checked={publishStatus}
          onChange={(e) => setPublishStatus(e.currentTarget.checked)}
        />

        <UnstyledButton onClick={() => setOverridesOpen((o) => !o)}>
          <Text size="sm" c="blue">
            {overridesOpen ? '▾' : '▸'} Check overrides{hasOverrides ? ' (active)' : ''}
          </Text>
        </UnstyledButton>
        <Collapse expanded={overridesOpen}>
          {overridesOpen && <CheckOverrides value={checkSettings} onChange={setCheckSettings} />}
        </Collapse>

        <UnstyledButton onClick={() => setAdvanced((a) => !a)}>
          <Text size="sm" c="blue">
            {advanced ? '▾' : '▸'} TLS settings
          </Text>
        </UnstyledButton>
        <Collapse expanded={advanced}>
          <Stack>
            <TextInput
              label="Certificate SHA-256 fingerprint (pin)"
              description="For self-signed certificates. When set, only this exact certificate is accepted."
              placeholder="ab:cd:ef:…"
              value={fingerprint}
              onChange={(e) => setFingerprint(e.currentTarget.value)}
            />
            <Textarea
              label="Custom CA certificate (PEM)"
              description={
                instance?.has_ca_pem && !clearCa
                  ? 'A CA is stored. Paste a new one to replace it.'
                  : 'Trust this CA instead of the system roots.'
              }
              placeholder="-----BEGIN CERTIFICATE-----"
              autosize
              minRows={3}
              maxRows={8}
              value={caPem}
              onChange={(e) => setCaPem(e.currentTarget.value)}
              styles={{ input: { fontFamily: 'monospace', fontSize: 12 } }}
            />
            {instance?.has_ca_pem && (
              <Switch label="Remove stored CA" checked={clearCa} onChange={(e) => setClearCa(e.currentTarget.checked)} />
            )}
            <Switch
              label="Allow insecure plain HTTP"
              description="The access token is sent unencrypted. Only use this on a trusted network."
              checked={insecure}
              onChange={(e) => setInsecure(e.currentTarget.checked)}
            />
          </Stack>
        </Collapse>
        {isHttp && (
          <Alert color={insecure ? 'orange' : 'red'} variant="light">
            {insecure
              ? 'This instance uses plain HTTP. The access token is not protected in transit.'
              : 'Plain HTTP URLs are rejected unless "Allow insecure plain HTTP" is enabled under TLS settings.'}
          </Alert>
        )}

        {test.data &&
          (test.data.ok ? (
            <Alert color="green" title="Connection successful">
              Home Assistant {test.data.ha_version}
              {test.data.location_name ? ` (${test.data.location_name})` : ''}
              {test.data.supervisor ? ', Supervisor available' : ''}.
              {!test.data.is_admin && ' Warning: the token is not an admin token, so actions and some checks will fail.'}
            </Alert>
          ) : (
            <Alert color="red" title="Connection failed">
              {test.data.error}
            </Alert>
          ))}
        {test.isError && <Alert color="red">{test.error.message}</Alert>}
        {save.isError && <Alert color="red">{save.error.message}</Alert>}

        <Group justify="space-between">
          <Button variant="default" onClick={() => test.mutate()} loading={test.isPending}>
            Test connection
          </Button>
          <Group>
            <Button variant="subtle" onClick={onCancel}>
              Cancel
            </Button>
            <Button type="submit" loading={save.isPending}>
              {editing ? 'Save' : 'Add instance'}
            </Button>
          </Group>
        </Group>
      </Stack>
    </form>
  )
}
