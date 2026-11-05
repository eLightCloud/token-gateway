/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
  RouterProvider,
  Outlet,
  Link,
} from '@tanstack/react-router'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { useState } from 'react'
import { afterEach, expect, it, vi } from 'vitest'

import { DataTableRowActionMenu } from '@/components/data-table'
import {
  UserPersonalPricingAction,
  UserPersonalPricingDialog,
} from '@/features/reseller/user-pricing-action'
import zhLocale from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import type { PricingWorkspace } from '../api'
import { RealtimePricingEditor } from '../editor'
import { PricingRuleDialog } from '../rule-dialog'

afterEach(async () => {
  cleanup()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
  await i18next.changeLanguage('en')
})

const candidates = [
  { id: 1, name: 'B', status: 1, models: 'A' },
  { id: 2, name: 'C', status: 1, models: 'A' },
  { id: 3, name: 'D', status: 1, models: 'other' },
]

function PersonalPricingActionFixture() {
  const [open, setOpen] = useState(false)
  return (
    <>
      <DataTableRowActionMenu ariaLabel='Actions'>
        <UserPersonalPricingAction onOpen={() => setOpen(true)} />
      </DataTableRowActionMenu>
      <UserPersonalPricingDialog
        userID={2}
        open={open}
        onOpenChange={setOpen}
      />
    </>
  )
}

function mountWorkspace(
  subject: 'user' | 'organization' = 'user',
  override: Partial<PricingWorkspace> = {},
  initialError?: unknown,
  action = false
) {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  const workspace: PricingWorkspace = {
    phase: 'active',
    config: {
      revision: 7,
      items: [{ model: 'A', ratio: '1', channel_ids: [1] }],
    },
    candidates,
    members: [{ id: 4, name: 'member' }],
    history: [],
    ...override,
  }
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: workspace },
  })
  if (action) {
    get.mockImplementation(async (path) => ({
      data: {
        success: true,
        data: path.startsWith('/api/realtime-pricing/')
          ? workspace
          : {
              ...workspace,
              pricing_phase: workspace.phase,
              config: {
                revision: workspace.config.revision,
                items: workspace.config.items.flatMap((rule) =>
                  rule.channel_ids.map((channel_id) => ({
                    model: rule.model,
                    ratio: rule.ratio,
                    channel_id,
                    authorized: true,
                  }))
                ),
              },
              candidates: candidates.flatMap((channel) =>
                channel.models
                  .split(',')
                  .map((model) => ({
                    model,
                    channel_id: channel.id,
                    authorized: true,
                    ratio: '1',
                  }))
              ),
            },
      },
    }))
  }
  if (initialError) get.mockRejectedValue(initialError)
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        rows: [{ model: 'A', channel_id: 1, ratio: '1', source: 'user' }],
        user_revision: 7,
        organization_revision: 1,
        generated_at: 1,
        user_id: 4,
        organization_id: 2,
      },
    },
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const root = createRootRoute({ component: Outlet })
  const index = createRoute({
    getParentRoute: () => root,
    path: '/',
    component: () => (
      <>
        {action ? (
          <PersonalPricingActionFixture />
        ) : (
          <RealtimePricingEditor subject={subject} subjectID={2} />
        )}
        <Link to='/dashboard'>Leave page</Link>
        <Link to='/about'>Switch subject</Link>
      </>
    ),
  })
  const destination = createRoute({
    getParentRoute: () => root,
    path: '/dashboard',
    component: () => <p>Destination</p>,
  })
  const other = createRoute({
    getParentRoute: () => root,
    path: '/about',
    component: () => <RealtimePricingEditor subject='user' subjectID={3} />,
  })
  const router = createRouter({
    routeTree: root.addChildren([index, destination, other]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return { post, client, get, workspace }
}

it('edits one model with multiple matching channels and one ratio without adding unrelated channels', async () => {
  const apply = vi.fn()
  render(
    <PricingRuleDialog
      rule={{ model: 'A', ratio: '1', channel_ids: [1] }}
      creating={false}
      models={['A']}
      candidates={candidates}
      onApply={apply}
      onClose={vi.fn()}
      onDirtyChange={vi.fn()}
    />
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Select matching channels' })
  )
  await userEvent.clear(screen.getByLabelText('Ratio'))
  await userEvent.type(screen.getByLabelText('Ratio'), '5')
  await userEvent.click(screen.getByRole('button', { name: 'Apply to draft' }))
  expect(apply).toHaveBeenCalledWith(
    { model: 'A', ratio: '5', channel_ids: [1, 2] },
    expect.anything()
  )
})

it('rejects excess precision and an empty channel range instead of silently rounding or inheriting', async () => {
  const apply = vi.fn()
  render(
    <PricingRuleDialog
      rule={{ model: 'A', ratio: '0.8', channel_ids: [1] }}
      creating={false}
      models={['A']}
      candidates={candidates}
      onApply={apply}
      onClose={vi.fn()}
      onDirtyChange={vi.fn()}
    />
  )
  await userEvent.clear(screen.getByLabelText('Ratio'))
  await userEvent.type(screen.getByLabelText('Ratio'), '0.001')
  expect(screen.getByRole('button', { name: 'Apply to draft' })).toBeDisabled()
  expect(screen.getByLabelText('Ratio')).toHaveAttribute('aria-invalid', 'true')
  await userEvent.clear(screen.getByLabelText('Ratio'))
  await userEvent.type(screen.getByLabelText('Ratio'), '0.01')
  await userEvent.click(screen.getByRole('button', { name: 'Clear' }))
  expect(screen.getByRole('button', { name: 'Apply to draft' })).toBeDisabled()
  expect(apply).not.toHaveBeenCalled()
})

it('deletes an explicit base-price rule only after confirmation and submits the subject revision', async () => {
  mountWorkspace()
  const put = vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: true, data: { revision: 8, items: [] } },
  })
  await userEvent.click(await screen.findByRole('button', { name: 'Delete' }))
  expect(put).not.toHaveBeenCalled()
  expect(
    await screen.findByText(
      'Removing this rule restores organization or system pricing on its channels. Changes take effect after saving.'
    )
  ).toBeVisible()
  await userEvent.click(await screen.findByRole('button', { name: 'Continue' }))
  await userEvent.click(screen.getByRole('button', { name: 'Save changes' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith('/api/realtime-pricing/user/2', {
      expected_revision: 7,
      items: [],
    })
  )
})

it('keeps the draft when a concurrent save returns a revision conflict', async () => {
  mountWorkspace()
  vi.spyOn(api, 'put').mockResolvedValue({
    data: {
      success: false,
      code: 'RESELLER_REVISION_CONFLICT',
      data: { revision: 8 },
    },
  })
  await userEvent.click(await screen.findByRole('button', { name: 'Delete' }))
  await userEvent.click(await screen.findByRole('button', { name: 'Continue' }))
  await userEvent.click(screen.getByRole('button', { name: 'Save changes' }))
  expect(
    await screen.findByText(
      'Configuration changed on the server (revision 8). Your draft is preserved.'
    )
  ).toBeVisible()
  expect(screen.getByText('No models configured')).toBeVisible()
  expect(
    screen.getByRole('button', { name: 'Reload configuration' })
  ).toBeVisible()
})

it('distinguishes organization-only preview from a selected member preview', async () => {
  const { post } = mountWorkspace('organization')
  expect(
    await screen.findByRole('heading', {
      name: 'Organization preview without personal overrides',
    })
  ).toBeVisible()
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/realtime-pricing/organization/2/preview',
      expect.objectContaining({ user_id: 0 })
    )
  )
  await userEvent.click(
    screen.getByPlaceholderText('Select member for personal overrides')
  )
  await userEvent.click(
    await screen.findByRole('option', { name: 'member #4' })
  )
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/realtime-pricing/organization/2/preview',
      expect.objectContaining({ user_id: 4 })
    )
  )
  expect(
    screen.getByRole('heading', { name: 'Effective pricing preview' })
  ).toBeVisible()
})

it('renders both frozen preview revisions and organization context', async () => {
  mountWorkspace()
  expect(await screen.findByText('User revision 7')).toBeVisible()
  expect(screen.getByText('Organization revision 1')).toBeVisible()
  expect(screen.getByText('Organization #2')).toBeVisible()
})

it('blocks real navigation with a draft and supports staying then leaving', async () => {
  mountWorkspace()
  await userEvent.click(await screen.findByRole('button', { name: 'Delete' }))
  await userEvent.click(await screen.findByRole('button', { name: 'Continue' }))
  await userEvent.click(screen.getByRole('link', { name: 'Leave page' }))
  await userEvent.click(await screen.findByRole('button', { name: 'Stay' }))
  expect(screen.getByRole('button', { name: 'Save changes' })).toBeEnabled()
  await userEvent.click(screen.getByRole('link', { name: 'Leave page' }))
  await userEvent.click(await screen.findByRole('button', { name: /^Leave$/ }))
  expect(await screen.findByText('Destination')).toBeVisible()
})

it('shows discount and markup meaning while editing', async () => {
  render(
    <PricingRuleDialog
      rule={{ model: 'A', ratio: '1', channel_ids: [1] }}
      creating={false}
      models={['A']}
      candidates={candidates}
      onApply={vi.fn()}
      onClose={vi.fn()}
      onDirtyChange={vi.fn()}
    />
  )
  await userEvent.clear(screen.getByLabelText('Ratio'))
  await userEvent.type(screen.getByLabelText('Ratio'), '0.8')
  expect(await screen.findByText('0.8× · 20% discount')).toBeVisible()
  await userEvent.clear(screen.getByLabelText('Ratio'))
  await userEvent.type(screen.getByLabelText('Ratio'), '1.5')
  expect(await screen.findByText('1.5× · 50% markup')).toBeVisible()
})

it('keeps a no-op edit clean even when decimal spelling changes', async () => {
  mountWorkspace('user', {
    config: {
      revision: 7,
      items: [
        { model: 'A', ratio: '0.8', channel_ids: [1] },
        { model: 'other', ratio: '1', channel_ids: [3] },
      ],
    },
  })
  await userEvent.click(
    (await screen.findAllByRole('button', { name: 'Edit' }))[0]
  )
  await userEvent.clear(screen.getByLabelText('Ratio'))
  await userEvent.type(screen.getByLabelText('Ratio'), '0.80')
  await userEvent.click(screen.getByRole('button', { name: 'Apply to draft' }))
  expect(screen.getByRole('button', { name: 'Save changes' })).toBeDisabled()
})

it('opens an already configured model from the add selector instead of adding a duplicate', async () => {
  mountWorkspace()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Add model' })
  )
  await userEvent.click(screen.getByPlaceholderText('Select model'))
  await userEvent.click(
    await screen.findByRole('option', { name: 'A · Configured' })
  )
  expect(screen.getByLabelText('Ratio')).toHaveValue('1')
  await userEvent.click(screen.getByRole('button', { name: 'Apply to draft' }))
  expect(screen.getByRole('button', { name: 'Save changes' })).toBeDisabled()
})

it('shows ratio and channel changes with the operator in immutable history', async () => {
  mountWorkspace('organization', {
    history: [
      {
        id: 1,
        revision: 2,
        operator_id: 1,
        operator_name: 'operator',
        created_at: 1,
        items: [{ model: 'A', ratio: '1.5', channel_ids: [2] }],
        previous_items: [{ model: 'A', ratio: '0.8', channel_ids: [1] }],
      },
    ],
  })
  await userEvent.click(
    await screen.findByRole('button', { name: 'Configuration history' })
  )
  expect(screen.getByText(/Channels added: C #2/)).toBeVisible()
  expect(screen.getByText(/Channels removed: B #1/)).toBeVisible()
  expect(screen.getByText(/1.5× · 50% markup/)).toBeVisible()
  expect(screen.getByText(/operator/)).toBeVisible()
})

it('preserves selected channel labels and off-filter selections while searching', async () => {
  const apply = vi.fn()
  render(
    <PricingRuleDialog
      rule={{ model: 'A', ratio: '1', channel_ids: [1] }}
      creating={false}
      models={['A']}
      candidates={candidates}
      onApply={apply}
      onClose={vi.fn()}
      onDirtyChange={vi.fn()}
    />
  )
  await userEvent.type(screen.getByLabelText('Search channels'), 'C')
  expect(screen.getByText('B #1')).toBeVisible()
  await userEvent.click(
    screen.getByRole('button', { name: 'Select matching channels' })
  )
  await userEvent.click(screen.getByRole('button', { name: 'Apply to draft' }))
  expect(apply).toHaveBeenCalledWith(
    expect.objectContaining({ channel_ids: [1, 2] }),
    expect.anything()
  )
})

it('requires a new member choice after membership changes', async () => {
  const { post, get, workspace } = mountWorkspace('organization')
  await screen.findByRole('heading', {
    name: 'Organization preview without personal overrides',
  })
  post.mockImplementation(
    async (_path, body) =>
      ({
        data: (body as { user_id: number })?.user_id
          ? { success: false, code: 'REALTIME_PRICING_MEMBER_CHANGED' }
          : {
              success: true,
              data: {
                rows: [],
                user_revision: 0,
                organization_revision: 1,
                organization_id: 2,
                user_id: 0,
                generated_at: 1,
              },
            },
      }) as never
  )
  await userEvent.click(
    screen.getByPlaceholderText('Select member for personal overrides')
  )
  await userEvent.click(
    await screen.findByRole('option', { name: 'member #4' })
  )
  expect(
    await screen.findByText(
      'This user is no longer a current organization member. Select another member.'
    )
  ).toBeVisible()
  get.mockResolvedValue({
    data: { success: true, data: { ...workspace, members: [] } },
  })
  await userEvent.click(
    screen.getByRole('button', { name: 'Select another member' })
  )
  expect(
    await screen.findByRole('heading', {
      name: 'Organization preview without personal overrides',
    })
  ).toBeVisible()
})

it('refreshes the server revision without overwriting a draft or silently rebasing its save', async () => {
  const { client, get, workspace } = mountWorkspace()
  await userEvent.click(await screen.findByRole('button', { name: 'Delete' }))
  await userEvent.click(await screen.findByRole('button', { name: 'Continue' }))
  get.mockResolvedValue({
    data: {
      success: true,
      data: {
        ...workspace,
        config: { revision: 8, items: workspace.config.items },
      },
    },
  })
  await client.invalidateQueries({ queryKey: ['realtime-pricing'] })
  expect(
    await screen.findByText(
      'Configuration changed on the server (revision 8). Your draft is preserved.'
    )
  ).toBeVisible()
  expect(screen.getByText('No models configured')).toBeVisible()
  const put = vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: true, data: { revision: 9, items: [] } },
  })
  await userEvent.click(screen.getByRole('button', { name: 'Save changes' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/realtime-pricing/user/2',
      expect.objectContaining({ expected_revision: 7 })
    )
  )
})

it('renders Chinese discount meaning and responds when the language changes', async () => {
  render(
    <PricingRuleDialog
      rule={{ model: 'A', ratio: '0.8', channel_ids: [1] }}
      creating={false}
      models={['A']}
      candidates={candidates}
      onApply={vi.fn()}
      onClose={vi.fn()}
      onDirtyChange={vi.fn()}
    />
  )
  i18next.addResourceBundle('zh', 'translation', zhLocale.translation)
  await i18next.changeLanguage('zh')
  expect(await screen.findByText('0.8× · 8 折')).toBeVisible()
  await i18next.changeLanguage('en')
  expect(await screen.findByText('0.8× · 20% discount')).toBeVisible()
})

it('hides cached editing controls after a permission rejection', async () => {
  const { client, get } = mountWorkspace()
  await screen.findByRole('button', { name: 'Add model' })
  get.mockRejectedValue({ response: { status: 403 } })
  await client.invalidateQueries({ queryKey: ['realtime-pricing'] })
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'You do not have permission to manage realtime pricing.'
  )
  expect(
    screen.queryByRole('button', { name: 'Add model' })
  ).not.toBeInTheDocument()
})

it('retries an initial loading failure without presenting an empty editable configuration', async () => {
  const { get, workspace } = mountWorkspace('user', {}, new Error('offline'))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Failed to load data'
  )
  expect(
    screen.queryByRole('button', { name: 'Add model' })
  ).not.toBeInTheDocument()
  get.mockResolvedValue({ data: { success: true, data: workspace } })
  await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByRole('button', { name: 'Add model' })).toBeEnabled()
})

it('does not carry a discarded draft into another subject', async () => {
  mountWorkspace()
  await userEvent.click(await screen.findByRole('button', { name: 'Delete' }))
  await userEvent.click(await screen.findByRole('button', { name: 'Continue' }))
  await userEvent.click(screen.getByRole('link', { name: 'Switch subject' }))
  await userEvent.click(await screen.findByRole('button', { name: /^Leave$/ }))
  expect(await screen.findByRole('button', { name: 'Edit' })).toBeVisible()
  expect(screen.getByRole('button', { name: 'Save changes' })).toBeDisabled()
  expect(screen.queryByText('No models configured')).not.toBeInTheDocument()
})

it('protects unsaved modal input when Escape is pressed and preserves input on cancel', async () => {
  const close = vi.fn()
  render(
    <PricingRuleDialog
      rule={{ model: 'A', ratio: '1', channel_ids: [1] }}
      creating={false}
      models={['A']}
      candidates={candidates}
      onApply={vi.fn()}
      onClose={close}
      onDirtyChange={vi.fn()}
    />
  )
  await userEvent.clear(screen.getByLabelText('Ratio'))
  await userEvent.type(screen.getByLabelText('Ratio'), '0.9')
  await userEvent.keyboard('{Escape}')
  expect(await screen.findByText('Discard this rule edit?')).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(screen.getByLabelText('Ratio')).toHaveValue('0.9')
  expect(close).not.toHaveBeenCalled()
})

it('hides the legacy editor after activation and protects the enclosing user dialog draft', async () => {
  mountWorkspace('user', {}, undefined, true)
  await userEvent.click(await screen.findByRole('button', { name: 'Actions' }))
  await userEvent.click(
    await screen.findByRole('menuitem', { name: 'Personal ratio' })
  )
  await userEvent.click(await screen.findByRole('button', { name: 'Delete' }))
  expect(
    screen.queryByText(
      'Ratios must be greater than 0 and at most 2, with up to six decimal places. Missing ratios use 1.'
    )
  ).not.toBeInTheDocument()
  await userEvent.click(await screen.findByRole('button', { name: 'Continue' }))
  await userEvent.click(await screen.findByRole('button', { name: 'Close' }))
  expect(await screen.findByRole('alertdialog')).toHaveTextContent(
    'You have unsaved changes'
  )
  await userEvent.click(screen.getByRole('button', { name: 'Stay' }))
  expect(screen.getByRole('button', { name: 'Save changes' })).toBeEnabled()
  await userEvent.keyboard('{Escape}')
  await userEvent.click(await screen.findByRole('button', { name: /^Leave$/ }))
  await waitFor(() =>
    expect(
      screen.queryByRole('dialog', { name: 'Personal ratio' })
    ).not.toBeInTheDocument()
  )
})

it('retains ratio, availability and editing actions in the mobile card layout', async () => {
  const width = window.innerWidth
  Object.defineProperty(window, 'innerWidth', {
    configurable: true,
    value: 390,
  })
  try {
    mountWorkspace('user', {
      config: {
        revision: 7,
        items: [{ model: 'A', ratio: '0.8', channel_ids: [1, 99] }],
      },
    })
    await waitFor(() =>
      expect(screen.getByText('Some channels unavailable')).toBeVisible()
    )
    expect(screen.getByText('0.8× · 20% discount')).toBeVisible()
    expect(
      screen.queryByRole('columnheader', { name: 'Status' })
    ).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Edit' }))
    expect(screen.getByLabelText('Ratio')).toHaveValue('0.8')
  } finally {
    Object.defineProperty(window, 'innerWidth', {
      configurable: true,
      value: width,
    })
  }
})

it('keeps the legacy draft protected when a clean realtime editor mounts after activation', async () => {
  const { get, client, workspace } = mountWorkspace(
    'user',
    { phase: 'legacy' },
    undefined,
    true
  )
  await userEvent.click(await screen.findByRole('button', { name: 'Actions' }))
  await userEvent.click(
    await screen.findByRole('menuitem', { name: 'Personal ratio' })
  )
  const ratio = await screen.findByLabelText('Ratio for A channel 1')
  await userEvent.clear(ratio)
  await userEvent.type(ratio, '0.9')
  get.mockResolvedValue({
    data: { success: true, data: { ...workspace, phase: 'active' } },
  })
  await client.invalidateQueries({ queryKey: ['realtime-pricing'] })
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Add model' })).toBeEnabled()
  )
  await userEvent.click(screen.getByRole('button', { name: 'Close' }))
  expect(await screen.findByRole('alertdialog')).toHaveTextContent(
    'You have unsaved changes'
  )
})

it('does not describe an unknown preview source as system default', async () => {
  const { post, client } = mountWorkspace()
  await screen.findByText('User revision 7')
  post.mockResolvedValue({
    data: {
      success: true,
      data: {
        rows: [
          { model: 'A', channel_id: 1, ratio: '1', source: 'future-source' },
        ],
        user_revision: 7,
        organization_revision: 1,
        user_id: 4,
        organization_id: 2,
        generated_at: 1,
      },
    },
  })
  await client.invalidateQueries({ queryKey: ['realtime-preview'] })
  expect(await screen.findByText('Unknown pricing source')).toBeVisible()
  expect(screen.queryByText('System default')).not.toBeInTheDocument()
})
