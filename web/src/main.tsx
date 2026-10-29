import '@mantine/core/styles.css'
import '@mantine/notifications/styles.css'

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { MantineProvider } from '@mantine/core'
import { Notifications } from '@mantine/notifications'
import { QueryCache, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ApiError } from './api'
import App from './App'

const queryClient: QueryClient = new QueryClient({
  queryCache: new QueryCache({
    onError: (err, query) => {
      // Any 401 means the session expired: re-check auth, which shows the login page.
      if (err instanceof ApiError && err.status === 401 && query.queryKey[0] !== 'me') {
        queryClient.invalidateQueries({ queryKey: ['me'] })
      }
    },
  }),
  defaultOptions: {
    queries: { retry: (n, err) => !(err instanceof ApiError && err.status < 500) && n < 2 },
  },
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <MantineProvider defaultColorScheme="auto">
      <Notifications position="top-right" />
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </QueryClientProvider>
    </MantineProvider>
  </StrictMode>,
)
