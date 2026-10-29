import { useState } from 'react'
import { Alert, Button, Center, Paper, PasswordInput, Stack, TextInput, Title } from '@mantine/core'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '../api'

export default function Login() {
  const qc = useQueryClient()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const login = useMutation({
    mutationFn: () => api.login(username, password),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['me'] }),
  })

  return (
    <Center h="100vh">
      <Paper withBorder shadow="sm" p="xl" w={360}>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            login.mutate()
          }}
        >
          <Stack>
            <Title order={3}>HA Fleet</Title>
            {login.isError && <Alert color="red">{login.error.message}</Alert>}
            <TextInput
              label="Username"
              autoComplete="username"
              value={username}
              onChange={(e) => setUsername(e.currentTarget.value)}
              required
              autoFocus
            />
            <PasswordInput
              label="Password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.currentTarget.value)}
              required
            />
            <Button type="submit" loading={login.isPending}>
              Log in
            </Button>
          </Stack>
        </form>
      </Paper>
    </Center>
  )
}
