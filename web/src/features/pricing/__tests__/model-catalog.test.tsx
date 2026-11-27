import {
  focusManager,
  QueryClient,
  QueryClientProvider,
} from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { Pricing } from '..'
import { ModelDetails } from '../components/model-details'
import { useModelCatalog } from '../hooks/use-model-catalog'

vi.mock('@/hooks/use-status', () => ({
  useStatus: () => ({ status: undefined }),
}))
vi.mock('@/components/layout', () => ({
  PublicLayout: ({ children }: { children: ReactNode }) => children,
}))
vi.mock('@visactor/react-vchart', () => ({ VChart: () => null }))
vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  useSearch: () => ({}),
  useParams: () => ({ modelId: 'example-model' }),
  useNavigate: () => vi.fn(),
}))

afterEach(() => {
  focusManager.setFocused(undefined)
  useAuthStore.getState().auth.reset()
})

test('catalog isolates visitor and customer data and refreshes on focus', async () => {
  let visitorModel = 'domestic'
  const getCatalog = vi.spyOn(api, 'get').mockImplementation(async () => {
    const customer = useAuthStore.getState().auth.user
    return {
      data: {
        success: true,
        data: [{ model_name: customer ? 'foreign-discounted' : visitorModel }],
        vendors: [],
      },
    }
  })
  const client = new QueryClient({
    defaultOptions: {
      queries: { staleTime: Infinity, refetchOnWindowFocus: false },
    },
  })
  function CatalogNames() {
    const { models } = useModelCatalog()
    return <output>{models.map((model) => model.model_name).join(', ')}</output>
  }
  render(
    <QueryClientProvider client={client}>
      <CatalogNames />
    </QueryClientProvider>
  )

  expect(await screen.findByText('domestic')).toBeInTheDocument()
  act(() => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 17, username: 'customer', role: 1 })
  })
  expect(screen.queryByText('domestic')).not.toBeInTheDocument()
  expect(await screen.findByText('foreign-discounted')).toBeInTheDocument()

  act(() => {
    useAuthStore.getState().auth.reset()
  })
  expect(screen.queryByText('foreign-discounted')).not.toBeInTheDocument()
  expect(await screen.findByText('domestic')).toBeInTheDocument()

  visitorModel = 'full-public-catalog'
  fireEvent.focus(window)
  expect(await screen.findByText('full-public-catalog')).toBeInTheDocument()
  await waitFor(() =>
    expect(getCatalog).toHaveBeenLastCalledWith(
      '/api/pricing/catalog',
      expect.anything()
    )
  )
})

test('an empty discounted catalog renders the customer empty state', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: [],
      vendors: [],
      visibility_filter: 'discounted',
    },
  })
  useAuthStore
    .getState()
    .auth.setUser({ id: 17, username: 'customer', role: 1 })
  const client = new QueryClient()
  render(
    <QueryClientProvider client={client}>
      <Pricing />
    </QueryClientProvider>
  )

  expect(
    await screen.findByText('No discounted models available')
  ).toBeInTheDocument()
})

const exampleCatalog = {
  success: true,
  data: [
    {
      model_name: 'example-model',
      model_ratio: 1,
      completion_ratio: 1,
      enable_groups: ['default'],
      supported_endpoint_types: [],
    },
  ],
  vendors: [],
}

test.each([
  { name: 'catalog list', Component: Pricing, status: 403 },
  { name: 'model detail', Component: ModelDetails, status: 403 },
  { name: 'catalog list', Component: Pricing, status: 401 },
  { name: 'model detail', Component: ModelDetails, status: 401 },
])(
  '$name hides cached models after HTTP $status and recovers on retry',
  async ({ Component, status }) => {
    let denied = false
    vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/pricing/catalog') {
        if (denied) throw { response: { status } }
        return { data: exampleCatalog }
      }
      return { data: { success: true, data: {} } }
    })
    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <Component />
      </QueryClientProvider>
    )

    expect(await screen.findByText('example-model')).toBeVisible()
    denied = true
    act(() => focusManager.setFocused(false))
    act(() => focusManager.setFocused(true))
    expect(await screen.findByText('Failed to load models')).toBeVisible()
    expect(screen.queryByText('example-model')).not.toBeInTheDocument()
    expect(screen.queryByText('Model not found')).not.toBeInTheDocument()

    denied = false
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('example-model')).toBeVisible()
  }
)

test('the agent catalog shows model pricing without requesting performance data', async () => {
  useAuthStore.getState().auth.setUser({
    id: 17,
    username: 'agent',
    role: 1,
    reseller_kind: 'agent',
  })
  const requests = vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/pricing/catalog') return { data: exampleCatalog }
    return { data: { success: true, data: {} } }
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const { rerender } = render(
    <QueryClientProvider client={client}>
      <Pricing />
    </QueryClientProvider>
  )
  expect(await screen.findByText('example-model')).toBeVisible()
  expect(requests).not.toHaveBeenCalledWith(
    '/api/perf-metrics/summary',
    expect.anything()
  )

  rerender(
    <QueryClientProvider client={client}>
      <ModelDetails />
    </QueryClientProvider>
  )
  expect(await screen.findByRole('tab', { name: 'Overview' })).toBeVisible()
  expect(
    screen.queryByRole('tab', { name: 'Performance' })
  ).not.toBeInTheDocument()
  expect(requests).not.toHaveBeenCalledWith(
    '/api/perf-metrics',
    expect.anything()
  )
})
