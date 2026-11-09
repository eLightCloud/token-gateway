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
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AxiosError } from 'axios'
import i18next from 'i18next'
import { afterEach, expect, it, vi } from 'vitest'

import { LanguageSwitcher } from '@/components/language-switcher'
import * as passwordEncryption from '@/features/auth/lib/password-encryption'
import { toIntlLocale } from '@/i18n/languages'
import { api } from '@/lib/api'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { formatNumber } from '@/lib/format'
import { useAuthStore } from '@/stores/auth-store'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { ResellerOwnAccountPage } from '../agent-account-page'
import { resellerAgentSearchSchema } from '../agent-status'
import { ResellerConfigEditor } from '../config-editor'
import { guardResellerRoute } from '../navigation'
import {
  PersonalRatioBadge,
  PersonalPricingHistory,
  ResellerWallet,
} from '../personal'
import { ResellerQuotaDialog } from '../quota-dialog'
import { ProcurementSummary } from '../shared'
import { ResellerUsageFacts } from '../usage-facts'
import { ResellerUsersPage } from '../users-page'
import { ResellerAdjustments, ResellerWorkspacePage } from '../workspace-page'

const initialSystemConfig = useSystemConfigStore.getState().config

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

vi.mock('@/features/system-settings/components/form-navigation-guard', () => ({
  FormNavigationGuard: () => null,
}))

vi.mock('@/features/security/components/two-fa-card', () => ({
  TwoFACard: () => null,
}))

afterEach(async () => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  useAuthStore.getState().auth.reset()
  useSystemConfigStore.setState({ config: initialSystemConfig })
  await i18next.changeLanguage('en')
})
function provider() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return ({ children }: { children: React.ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  )
}

it('closes the reseller dialog before mail delivery and lets the administrator retry from the row', async () => {
  let failMail: () => void = () => {}
  const mail = new Promise<Awaited<ReturnType<typeof api.post>>>((resolve) => {
    failMail = () =>
      resolve({
        data: { success: false, code: 'EMAIL_BINDING_DELIVERY_FAILED' },
      } as Awaited<ReturnType<typeof api.post>>)
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValueOnce({ data: { success: true, data: { user_id: 2 } } })
    .mockReturnValueOnce(mail)
    .mockResolvedValueOnce({ data: { success: true, data: null } })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        items: [
          {
            user_id: 2,
            username: 'new-agent',
            email: 'agent@example.invalid',
            phone: '+8613800138000',
            activation: 'pending',
            quota: 0,
          },
          {
            user_id: 3,
            username: 'enrolling-agent',
            email: 'enrolling@example.invalid',
            activation: 'security_pending',
            quota: 0,
          },
        ],
      },
    },
  })
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => <ResellerUsersPage admin />,
    }),
    history: createMemoryHistory(),
  })
  render(<RouterProvider router={router} />, { wrapper: provider() })

  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'Create reseller' })
  )
  const dialog = within(await screen.findByRole('dialog'))
  const email = dialog.getByRole('textbox', { name: 'Email' })
  const phone = dialog.getByRole('textbox', { name: 'Mobile number' })
  await user.type(
    dialog.getByRole('textbox', { name: 'Username' }),
    'new-agent'
  )
  await user.click(dialog.getByRole('button', { name: 'Create' }))
  expect(email).toHaveAttribute('aria-invalid', 'true')
  expect(phone).toHaveAttribute('aria-invalid', 'true')
  expect(post).not.toHaveBeenCalled()

  await user.type(email, 'agent@example.invalid')
  await user.type(phone, '+8613800138000')
  await user.type(dialog.getByLabelText('Remark'), '华东代理')
  await user.click(dialog.getByRole('button', { name: 'Create' }))
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  expect(post).toHaveBeenNthCalledWith(
    1,
    '/api/reseller/admin/agents?send_activation_email=false',
    {
      username: 'new-agent',
      display_name: '',
      email: 'agent@example.invalid',
      phone: '+8613800138000',
      remark: '华东代理',
    }
  )
  expect(await screen.findByText('+8613800138000')).toBeInTheDocument()
  expect(post).toHaveBeenNthCalledWith(
    2,
    '/api/reseller/admin/agents/2/activation',
    {}
  )
  const pendingRow = within(screen.getByRole('row', { name: /new-agent/ }))
  expect(
    await pendingRow.findByRole('button', { name: 'Sending...' })
  ).toBeDisabled()
  expect(
    pendingRow.queryByRole('button', { name: 'Reset Password' })
  ).toBeNull()

  await act(failMail)
  await user.click(
    await pendingRow.findByRole('button', { name: 'Reset Password' })
  )
  expect(post).toHaveBeenNthCalledWith(
    3,
    '/api/reseller/admin/agents/2/activation',
    {}
  )
})

it('shows only password setup and two activation states on the reseller list', async () => {
  const user = userEvent.setup()
  const agents = [
    {
      user_id: 2,
      username: 'enrolling-agent',
      activation: 'security_pending',
      account: { enabled: true, balance: 0, reserved: 0 },
    },
    {
      user_id: 3,
      username: 'suspended-agent',
      activation: 'active',
      platform_disabled: true,
      account: { enabled: true, balance: 0, reserved: 0 },
    },
  ]
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { items: agents } },
  })
  const router = createRouter({
    routeTree: createRootRoute({
      validateSearch: resellerAgentSearchSchema,
      component: () => <ResellerUsersPage admin />,
    }),
    history: createMemoryHistory({
      initialEntries: [
        '/?activation=security_pending&service_status=platform_disabled&after=42',
      ],
    }),
  })
  render(<RouterProvider router={router} />, { wrapper: provider() })

  const activation = await screen.findByRole('combobox', { name: 'Activation' })
  expect(
    within(activation)
      .getAllByRole('option')
      .map((option) => option.textContent)
  ).toEqual(['All', 'Activated', 'Disabled'])
  const password = screen.getByRole('combobox', { name: 'Password' })
  expect(
    within(password)
      .getAllByRole('option')
      .map((option) => option.textContent)
  ).toEqual(['All', 'Password not set', 'Configured'])
  const enrolling = within(
    await screen.findByRole('row', { name: /enrolling-agent/ })
  )
  expect(enrolling.getByRole('cell', { name: 'Configured' })).toBeVisible()
  expect(enrolling.getByRole('cell', { name: 'Activated' })).toBeVisible()
  const suspended = within(screen.getByRole('row', { name: /suspended-agent/ }))
  expect(suspended.getByRole('cell', { name: 'Disabled' })).toBeVisible()
  expect(suspended.queryByText('Platform disabled')).not.toBeInTheDocument()

  expect(activation).toHaveValue('disabled')
  expect(password).toHaveValue('password_set')
  expect(get).toHaveBeenLastCalledWith(
    expect.stringMatching(
      /after=0&activation=password_set&service_status=disabled$/
    )
  )
  await user.selectOptions(activation, 'enabled')
  await user.selectOptions(password, 'pending')
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith(
      expect.stringMatching(/activation=pending&service_status=enabled$/)
    )
  )
})

it('lets an authorized administrator add available quota from the reseller row', async () => {
  const admin = { id: 1, username: 'admin', role: 10 }
  useAuthStore.getState().auth.setUser(admin)
  const agent = {
    user_id: 2,
    username: 'quota-agent',
    email: 'agent@example.invalid',
    activation: 'active',
    account: { enabled: true, balance: 1_500_000, reserved: 500_000 },
  }
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValueOnce({
      data: { success: true, data: { items: [agent] } },
    })
    .mockResolvedValue({
      data: {
        success: true,
        data: {
          items: [
            { ...agent, account: { ...agent.account, balance: 2_000_000 } },
          ],
        },
      },
    })
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: {} },
  })
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => <ResellerUsersPage admin />,
    }),
    history: createMemoryHistory(),
  })
  render(<RouterProvider router={router} />, { wrapper: provider() })

  const row = within(await screen.findByRole('row', { name: /quota-agent/ }))
  expect(row.queryByRole('button', { name: 'Adjust Quota' })).toBeNull()
  act(() => {
    useAuthStore.getState().auth.setUser({
      ...admin,
      permissions: { admin_permissions: { reseller: { quota: true } } },
    })
  })
  const user = userEvent.setup()
  await user.click(await row.findByRole('button', { name: 'Adjust Quota' }))
  const dialog = within(
    await screen.findByRole('dialog', { name: 'Adjust Quota · quota-agent' })
  )
  await user.type(dialog.getByLabelText('Amount'), '1')
  await user.type(dialog.getByLabelText('Reason'), 'Allocate procurement quota')
  const locale = toIntlLocale('en')
  const expectedQuota = formatQuotaWithCurrency(1_500_000, { locale })
  expect(
    dialog.getByText(
      `${formatQuotaWithCurrency(1_000_000, { locale })} → ${expectedQuota}`
    )
  ).toBeInTheDocument()
  await user.click(dialog.getByRole('button', { name: 'Confirm' }))
  expect(post).toHaveBeenCalledWith(
    '/api/reseller/admin/agents/2/quota-adjustments',
    expect.objectContaining({
      mode: 'add',
      quota: 500_000,
      reason: 'Allocate procurement quota',
    })
  )
  expect(await row.findByText(expectedQuota)).toBeInTheDocument()
  expect(get).toHaveBeenCalledTimes(2)
})

it('changes an agent interface language without calling the forbidden profile endpoint', async () => {
  useAuthStore.getState().auth.setUser({
    id: 2,
    username: 'agent',
    role: 1,
    reseller_kind: 'agent',
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  render(<LanguageSwitcher />)
  await userEvent.click(screen.getByRole('button', { name: 'Change language' }))
  await userEvent.click(
    await screen.findByRole('menuitem', { name: 'English' })
  )
  expect(put).not.toHaveBeenCalled()
})

it('activates the reseller separately from sending a password reset email', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 100 })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        user: {
          user_id: 2,
          username: 'agent',
          activation: 'active',
          quota: 0,
          account: {
            user_id: 2,
            balance: 500000,
            reserved: 0,
            revision: 1,
            published_at: 0,
            enabled: false,
          },
        },
        config: { revision: 1, items: [] },
        candidates: [],
        history: [],
      },
    },
  })
  render(<ResellerWorkspacePage userID={2} admin />, { wrapper: provider() })
  await userEvent.click(
    await screen.findByRole('button', { name: 'Activate account' })
  )
  expect(await screen.findByRole('alertdialog')).toHaveTextContent(
    'Restoring this reseller keeps each customer’s independent restrictions in effect.'
  )
  expect(screen.getByRole('alertdialog')).not.toHaveTextContent(
    'Disabling this reseller'
  )
  await userEvent.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Continue',
    })
  )
  expect(put).toHaveBeenCalledWith('/api/reseller/admin/agents/2/enabled', {
    enabled: true,
  })
  expect(post).not.toHaveBeenCalled()
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  await userEvent.click(screen.getByRole('button', { name: 'Reset Password' }))
  await userEvent.click(
    within(
      screen.getByRole('alertdialog', { name: 'Reset Password' })
    ).getByRole('button', { name: 'Send' })
  )
  expect(post).toHaveBeenCalledWith(
    '/api/reseller/admin/agents/2/password-reset',
    {}
  )
})

it('keeps pending-email correction in the administrator activation action', async () => {
  const user = userEvent.setup()
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        user: {
          user_id: 2,
          username: 'agent',
          email: 'old@example.invalid',
          activation: 'pending',
          quota: 0,
        },
        config: { revision: 0, items: [] },
        candidates: [],
        history: [],
      },
    },
  })
  render(<ResellerWorkspacePage userID={2} admin />, { wrapper: provider() })
  const send = await screen.findByRole('button', {
    name: 'Reset Password',
  })
  const information = within(
    screen.getByRole('tabpanel', { name: 'Basic information' })
  )
  expect(information.getByText('old@example.invalid')).toBeVisible()
  expect(information.queryByRole('textbox')).not.toBeInTheDocument()
  await user.click(send)
  const dialog = within(screen.getByRole('dialog', { name: 'Set Password' }))
  const email = dialog.getByLabelText('Correct pending email')
  const submit = dialog.getByRole('button', {
    name: 'Send activation email',
  })
  await user.type(email, 'bad')
  await user.click(submit)
  expect(await dialog.findByText('Enter a valid email address')).toBeVisible()
  expect(post).not.toHaveBeenCalled()
  await user.clear(email)
  await user.type(email, 'new@example.invalid')
  await user.click(submit)
  expect(post).toHaveBeenCalledWith('/api/reseller/admin/agents/2/activation', {
    email: 'new@example.invalid',
  })
})

it('shows only saved active agent discounts until the administrator opens the editor', async () => {
  const user = userEvent.setup()
  const discount = {
    model: 'discounted-model',
    channel_id: 11,
    authorized: true,
    ratio: '0.3',
  }
  const revoked = {
    ...discount,
    channel_id: 12,
    authorized: false,
  }
  const fullPrice = { ...discount, model: 'full-price-model', ratio: '1' }
  const markup = { ...discount, model: 'markup-model', ratio: '1.2' }
  const removed = { ...discount, model: 'removed-model' }
  const candidate = { ...revoked, model: 'candidate-model', ratio: '1' }
  const workspace = {
    data: {
      success: true,
      data: {
        user: { user_id: 2, username: 'agent', activation: 'active', quota: 0 },
        config: {
          revision: 7,
          items: [discount, revoked, fullPrice, markup, removed],
        },
        candidates: [discount, revoked, fullPrice, markup, candidate],
        history: [],
      },
    },
  }
  vi.spyOn(api, 'get').mockImplementation(async (path) => {
    if (path.includes('/config/history')) {
      return {
        data: {
          success: true,
          data: {
            items: [
              {
                revision: 7,
                operator_id: 1,
                created_at: 100,
                changes: [
                  {
                    model: discount.model,
                    channel_id: discount.channel_id,
                    before: { authorized: true, ratio: '0.4' },
                    after: { authorized: true, ratio: '0.3' },
                  },
                ],
              },
            ],
            next_cursor: 0,
          },
        },
      }
    }
    return workspace
  })
  render(<ResellerWorkspacePage userID={2} admin />, { wrapper: provider() })
  const tab = await screen.findByRole('tab', {
    name: 'Model authorization and ratios',
  })
  await user.click(tab)
  const panel = within(
    screen.getByRole('tabpanel', { name: 'Model authorization and ratios' })
  )
  expect(panel.getAllByRole('row')).toHaveLength(2)
  expect(
    panel.getByRole('row', { name: /discounted-model.*11.*0\.3/ })
  ).toBeVisible()
  expect(panel.queryByText('candidate-model')).not.toBeInTheDocument()
  await user.click(
    await panel.findByRole('button', { name: 'View changes for Revision 7' })
  )
  const history = within(
    panel.getByRole('region', { name: 'Configuration history' })
  )
  expect(
    history.getByRole('row', { name: /discounted-model.*11.*0\.4.*0\.3/ })
  ).toBeVisible()
  await user.click(
    panel.getByRole('button', { name: 'View changes for Revision 7' })
  )

  const edit = panel.getByRole('button', { name: 'Edit' })
  expect(edit).toHaveAttribute('aria-expanded', 'false')
  edit.focus()
  await user.keyboard('{Enter}')
  const grant = panel.getByRole('checkbox', {
    name: 'Authorize candidate-model channel 12',
  })
  await user.click(grant)
  expect(grant).toBeChecked()
  const ratio = panel.getByLabelText('Ratio for candidate-model channel 12')
  await user.clear(ratio)
  await user.type(ratio, '-0.1')
  expect(ratio).toHaveAttribute('aria-invalid', 'true')
  expect(ratio).toHaveAccessibleDescription(
    'Enter a ratio greater than 0 and at most 2, with up to six decimal places.'
  )
  expect(
    within(panel.getByRole('row', { name: /candidate-model/ })).queryByText(
      'Effective'
    )
  ).not.toBeInTheDocument()
  expect(panel.getByRole('button', { name: 'Save changes' })).toBeDisabled()
  await user.clear(ratio)
  await user.type(ratio, '0.7')
  expect(ratio).toHaveAttribute('aria-invalid', 'false')
  expect(panel.getByRole('button', { name: 'Save changes' })).toBeEnabled()
  await user.click(panel.getByRole('button', { name: 'Collapse' }))
  expect(panel.queryByRole('checkbox')).not.toBeInTheDocument()
  expect(panel.getAllByRole('row')).toHaveLength(2)
  await user.click(panel.getByRole('button', { name: 'Edit' }))
  expect(grant).toBeChecked()
})

it('shows an empty discount list when the agent has only unconfigured candidates', async () => {
  const workspace = {
    data: {
      success: true,
      data: {
        user: { user_id: 2, username: 'agent', activation: 'active', quota: 0 },
        config: { revision: 0, items: [] },
        candidates: [
          { model: 'candidate', channel_id: 11, authorized: false, ratio: '1' },
        ],
        history: [],
      },
    },
  }
  vi.spyOn(api, 'get').mockImplementation(async (path) =>
    path.includes('/config/history')
      ? { data: { success: true, data: { items: [], next_cursor: 0 } } }
      : workspace
  )
  render(<ResellerWorkspacePage userID={2} admin />, { wrapper: provider() })
  await userEvent.click(
    await screen.findByRole('tab', { name: 'Model authorization and ratios' })
  )
  expect(screen.getByText('No active model discounts')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Edit' })).toBeEnabled()
})

it('lets an agent update their own contact details and view discounts read-only', async () => {
  useAuthStore.getState().auth.setUser({
    id: 2,
    username: 'agent',
    role: 1,
    reseller_kind: 'agent',
  })
  const user = userEvent.setup()
  const profile = {
    user_id: 2,
    username: 'agent',
    display_name: 'Agent name',
    phone: '13800138000',
    email: 'agent@example.invalid',
    activation: 'active',
    quota: 0,
  }
  vi.spyOn(api, 'get').mockImplementation(async () => ({
    data: {
      success: true,
      data: {
        user: { ...profile },
        discounts: [
          {
            model: 'configured-model',
            channel_id: 11,
            authorized: true,
            ratio: '0.8',
          },
        ],
      },
    },
  }))
  const put = vi.spyOn(api, 'put').mockImplementation(async () => {
    profile.display_name = 'Updated agent'
    return { data: { success: true } }
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true, data: null } })
  render(<ResellerOwnAccountPage />, { wrapper: provider() })
  expect(await screen.findByText('agent@example.invalid')).toBeVisible()
  expect(screen.queryByText('Remark')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Edit profile' }))
  const dialog = within(screen.getByRole('dialog'))
  expect(dialog.queryByLabelText('Email')).not.toBeInTheDocument()
  const name = dialog.getByLabelText('Display name')
  await user.clear(name)
  await user.type(name, 'Updated agent')
  await user.click(dialog.getByRole('button', { name: 'Save changes' }))
  expect(await screen.findByText('Updated agent')).toBeVisible()
  expect(put).toHaveBeenCalledWith('/api/reseller/self/account', {
    display_name: 'Updated agent',
  })
  await user.click(screen.getByRole('button', { name: 'Reset Password' }))
  await user.click(
    within(
      screen.getByRole('alertdialog', { name: 'Reset Password' })
    ).getByRole('button', { name: 'Send' })
  )
  expect(post).toHaveBeenCalledWith(
    '/api/reseller/self/account/password-reset',
    {}
  )
  await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
  await user.click(screen.getByRole('tab', { name: 'Model discounts' }))
  const discounts = within(
    screen.getByRole('tabpanel', { name: 'Model discounts' })
  )
  expect(
    discounts.getByRole('row', { name: /configured-model.*11.*0\.8/ })
  ).toBeVisible()
  expect(
    discounts.queryByRole('button', { name: 'Edit' })
  ).not.toBeInTheDocument()
})

it('lets an agent verify their identity and new email from their own account', async () => {
  useAuthStore.getState().auth.setUser({
    id: 2,
    username: 'agent',
    role: 1,
    reseller_kind: 'agent',
  })
  const user = userEvent.setup()
  vi.spyOn(passwordEncryption, 'encryptPassword').mockResolvedValue({
    password_encrypted: 'encrypted-password',
    encryption_key_id: 'test-key',
  })
  vi.spyOn(api, 'get').mockImplementation(async (path) => ({
    data: {
      success: true,
      data:
        path === '/api/reseller/self/account'
          ? {
              user: {
                user_id: 2,
                username: 'agent',
                email: 'old@example.com',
                activation: 'active',
                quota: 0,
              },
              discounts: [],
            }
          : {
              scope: 'account.binding.bind',
              methods: [{ method: '2fa', available: true }],
              oauth_providers: [],
              password_encryption_enabled: false,
            },
    },
  }))
  const post = vi.spyOn(api, 'post').mockImplementation(async (path) => {
    const now = Math.floor(Date.now() / 1000)
    if (path === '/api/verify') {
      return {
        data: {
          success: true,
          data: {
            scope: 'account.binding.bind',
            method: '2fa',
            proof_token: 'agent-email-proof',
            expires_at: now + 60,
          },
        },
      }
    }
    if (path === '/api/oauth/email/bind/start') {
      return {
        data: {
          success: true,
          data: {
            flow_token: 'agent-email-flow',
            email: 'new@example.com',
            old_email_required: false,
            expires_at: now + 600,
            resend_at: now + 60,
          },
        },
      }
    }
    return { data: { success: true, data: { sign_in_required: true } } }
  })
  const root = createRootRoute()
  const router = createRouter({
    routeTree: root.addChildren([
      createRoute({
        getParentRoute: () => root,
        path: '/',
        component: ResellerOwnAccountPage,
      }),
      createRoute({ getParentRoute: () => root, path: '/sign-in' }),
    ]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(<RouterProvider router={router} />, { wrapper: provider() })
  await user.click(await screen.findByRole('button', { name: 'Change Email' }))
  await user.type(screen.getByLabelText('Email Address'), 'new@example.com')
  await user.type(screen.getByLabelText('Current Password'), 'current-password')
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  await user.type(
    await screen.findByLabelText('Authenticator code or backup code'),
    '123456'
  )
  await user.click(screen.getByRole('button', { name: 'Verify' }))
  await user.type(
    await screen.findByLabelText('New email verification code'),
    '654321'
  )
  await user.click(screen.getByRole('button', { name: 'Confirm email' }))
  expect(post).toHaveBeenCalledWith(
    '/api/oauth/email/bind/start',
    {
      email: 'new@example.com',
      password_encrypted: 'encrypted-password',
      encryption_key_id: 'test-key',
    },
    expect.objectContaining({
      headers: { 'X-Security-Proof': 'agent-email-proof' },
    })
  )
  expect(post).toHaveBeenCalledWith(
    '/api/oauth/email/bind',
    { flow_token: 'agent-email-flow', new_code: '654321', old_code: '' },
    expect.objectContaining({ singleUseAuthorization: true })
  )
  await waitFor(() => expect(router.state.location.pathname).toBe('/sign-in'))
  expect(useAuthStore.getState().auth.user).toBeNull()
})

it('keeps personal model ranges private across anonymous and switched accounts', async () => {
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: [{ model: 'demo', min: '0.8', max: '1.2' }],
    },
  })
  const view = render(<PersonalRatioBadge model='demo' />, {
    wrapper: provider(),
  })
  expect(get).not.toHaveBeenCalled()
  act(() =>
    useAuthStore.getState().auth.setUser({
      id: 3,
      username: 'customer',
      role: 1,
      reseller_kind: 'agent_customer',
    })
  )
  expect(await screen.findByText('Personal ratio 0.8–1.2')).toBeVisible()
  get.mockResolvedValue({ data: { success: true, data: [] } })
  act(() =>
    useAuthStore
      .getState()
      .auth.setUser({ id: 4, username: 'ordinary', role: 1 })
  )
  await waitFor(() =>
    expect(screen.queryByText('Personal ratio 0.8–1.2')).not.toBeInTheDocument()
  )
  view.unmount()
})

it('restricted customer wallet requests no payment or subscription endpoints', async () => {
  useAuthStore.getState().auth.setUser({
    id: 3,
    username: 'customer',
    role: 1,
    reseller_kind: 'agent_customer',
  })
  const get = vi.spyOn(api, 'get').mockImplementation(async (path) => ({
    data: {
      success: true,
      data: path === '/api/user/self' ? { quota: 500000 } : [],
    },
  }))
  render(<ResellerWallet />, { wrapper: provider() })
  expect(await screen.findByText('Available quota')).toBeVisible()
  await waitFor(() => expect(get).toHaveBeenCalledTimes(2))
  expect(get.mock.calls.map(([path]) => path).sort()).toEqual([
    '/api/reseller/personal/history',
    '/api/user/self',
  ])
  expect(screen.queryByText('Top Up')).not.toBeInTheDocument()
  expect(() => guardResellerRoute('agent_customer', '/wallet/')).not.toThrow()
  expect(() =>
    guardResellerRoute('agent_customer', '/reseller/customers')
  ).toThrow()
  expect(() => guardResellerRoute('agent', '/keys')).toThrow()
  expect(() =>
    guardResellerRoute('agent', '/reseller/procurement')
  ).not.toThrow()
})

it.each(['HTTPS', 'HTTP'])(
  'requires adjustment reason and preserves retry identity in %s',
  async (protocol) => {
    if (protocol === 'HTTP') {
      vi.stubGlobal('crypto', {
        getRandomValues: globalThis.crypto.getRandomValues.bind(
          globalThis.crypto
        ),
      })
    }
    const post = vi
      .spyOn(api, 'post')
      .mockRejectedValueOnce(new Error('connection lost'))
      .mockResolvedValue({ data: { success: true, data: {} } })
    const success = vi.fn()
    render(
      <ResellerQuotaDialog
        open
        userID={3}
        admin={false}
        quota={500000}
        onOpenChange={() => undefined}
        onSuccess={success}
      />,
      { wrapper: provider() }
    )
    expect(screen.getByRole('button', { name: 'Confirm' })).toBeDisabled()
    await userEvent.type(screen.getByLabelText('Amount'), '1')
    expect(screen.getByRole('button', { name: 'Confirm' })).toBeDisabled()
    await userEvent.type(screen.getByLabelText('Reason'), 'Customer allocation')
    await userEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Confirm' })).toBeEnabled()
    )
    await userEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    await waitFor(() => expect(success).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledTimes(2)
    expect(post.mock.calls[0][0]).toBe(
      '/api/reseller/self/customers/3/quota-adjustments'
    )
    expect(post.mock.calls[0][1]).toEqual(post.mock.calls[1][1])
    expect(post.mock.calls[0][1]).toMatchObject({
      mode: 'add',
      quota: 500000,
      reason: 'Customer allocation',
    })
    const originalID = (post.mock.calls[0][1] as { operation_id: string })
      .operation_id
    expect(originalID).toMatch(/^[A-Za-z0-9_.:-]{1,64}$/)
    post.mockRejectedValueOnce(
      new Error('connection lost after payload change')
    )
    await userEvent.type(screen.getByLabelText('Reason'), ' revised')
    await userEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    await screen.findByRole('alert')
    expect(post).toHaveBeenCalledTimes(3)
    const changedID = (post.mock.calls[2][1] as { operation_id: string })
      .operation_id
    expect(changedID).toMatch(/^[A-Za-z0-9_.:-]{1,64}$/)
    expect(changedID).not.toBe(originalID)
  }
)

it('keeps a conflicting draft visible and never silently overwrites the server revision', async () => {
  const put = vi.spyOn(api, 'put').mockResolvedValue({
    data: {
      success: false,
      code: 'RESELLER_REVISION_CONFLICT',
      data: { revision: 8 },
    },
  })
  const saved = vi.fn()
  const items = [
    { model: 'demo', channel_id: 11, authorized: true, ratio: '0.9' },
  ]
  render(
    <ResellerConfigEditor
      userID={3}
      admin={false}
      config={{ revision: 7, items }}
      candidates={items}
      onSuccess={saved}
      onReload={async () => undefined}
    />,
    { wrapper: provider() }
  )
  const ratio = screen.getByLabelText('Ratio for demo channel 11')
  await userEvent.clear(ratio)
  await userEvent.type(ratio, '2.000001')
  expect(screen.getByRole('button', { name: 'Save changes' })).toBeDisabled()
  await userEvent.clear(ratio)
  await userEvent.type(ratio, '0.7')
  await userEvent.click(screen.getByRole('button', { name: 'Save changes' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Configuration changed on the server (revision 8). Your draft is preserved.'
  )
  expect(ratio).toHaveValue('0.7')
  expect(put).toHaveBeenCalledWith('/api/reseller/self/customers/3/config', {
    expected_revision: 7,
    items: [{ model: 'demo', channel_id: 11, authorized: true, ratio: '0.7' }],
  })
  expect(saved).not.toHaveBeenCalled()
})

it('supports keyboard radio selection and freezes controls during a quota submission', async () => {
  const user = userEvent.setup()
  const post = vi
    .spyOn(api, 'post')
    .mockImplementation(() => new Promise(() => undefined))
  render(
    <ResellerQuotaDialog
      open
      userID={3}
      admin={false}
      quota={500000}
      onOpenChange={() => undefined}
      onSuccess={() => undefined}
    />,
    { wrapper: provider() }
  )
  const group = screen.getByRole('radiogroup', { name: 'Operation' })
  const add = within(group).getByRole('radio', { name: 'Add' })
  const subtract = within(group).getByRole('radio', { name: 'Subtract' })
  await user.click(add)
  await user.keyboard('{ArrowRight}')
  expect(subtract).toBeChecked()
  await user.type(screen.getByLabelText('Amount'), '1')
  await user.type(screen.getByLabelText('Reason'), 'Withdraw allocation')
  await user.click(screen.getByRole('button', { name: 'Confirm' }))
  expect(post).toHaveBeenCalledWith(
    expect.anything(),
    expect.objectContaining({ mode: 'subtract', quota: 500000 })
  )
  expect(add).toHaveAttribute('aria-disabled', 'true')
  expect(subtract).toHaveAttribute('aria-disabled', 'true')
  expect(screen.getByLabelText('Amount')).toBeDisabled()
})

it('keeps checkbox authorization keyboard-accessible and disables revoked and saving rows', async () => {
  const user = userEvent.setup()
  const put = vi
    .spyOn(api, 'put')
    .mockImplementation(() => new Promise(() => undefined))
  const items = [
    { model: 'demo', channel_id: 11, authorized: true, ratio: '0.9' },
    { model: 'revoked', channel_id: 12, authorized: true, ratio: '0.8' },
  ]
  render(
    <ResellerConfigEditor
      userID={3}
      admin={false}
      config={{ revision: 7, items }}
      candidates={[items[0]]}
      onSuccess={() => undefined}
      onReload={async () => undefined}
    />,
    { wrapper: provider() }
  )
  const enabled = screen.getByRole('checkbox', {
    name: 'Authorize demo channel 11',
  })
  expect(
    screen.getByRole('checkbox', { name: 'Authorize revoked channel 12' })
  ).toHaveAttribute('aria-disabled', 'true')
  await user.click(enabled)
  expect(enabled).not.toBeChecked()
  await user.keyboard(' ')
  expect(enabled).toBeChecked()
  await user.click(enabled)
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  expect(put).toHaveBeenCalledWith(
    expect.anything(),
    expect.objectContaining({
      items: [
        expect.objectContaining({ model: 'revoked', authorized: true }),
        expect.objectContaining({ model: 'demo', authorized: false }),
      ],
    })
  )
  expect(enabled).toHaveAttribute('aria-disabled', 'true')
})

it.each(['zhCN', 'zhTW', 'en', 'fr', 'ja', 'ru', 'vi', 'invalid_locale!'])(
  'formats reseller numbers in %s without losing six-digit ratios',
  async (language) => {
    i18next.addResourceBundle(language, 'translation', {
      __reseller_locale_fixture__: 'fixture',
    })
    await i18next.changeLanguage(language)
    useSystemConfigStore
      .getState()
      .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
    useAuthStore.getState().auth.setUser({
      id: 3,
      username: 'customer',
      role: 1,
      reseller_kind: 'agent_customer',
    })
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: [{ model: 'demo', min: '0.000001', max: '1.234567' }],
      },
    })
    render(
      <>
        <ProcurementSummary
          account={{
            user_id: 2,
            balance: 1234567890,
            reserved: 12345000,
            revision: 1,
            published_at: 0,
            enabled: true,
          }}
        />
        <ResellerUsageFacts usage={{ input_tokens: 12345.678 }} />
        <PersonalRatioBadge model='demo' />
      </>,
      { wrapper: provider() }
    )
    const locale = toIntlLocale(language)
    expect(
      screen.getByText(`Input Tokens: ${formatNumber(12345.678, locale)}`, {
        normalizer: (value) => value,
      })
    ).toBeVisible()
    expect(
      screen.getByText(formatQuotaWithCurrency(1234567890, { locale }), {
        normalizer: (value) => value,
      })
    ).toBeVisible()
    const ratio = `${new Intl.NumberFormat(locale, { maximumFractionDigits: 6 }).format(0.000001)}–${new Intl.NumberFormat(locale, { maximumFractionDigits: 6 }).format(1.234567)}`
    expect(await screen.findByText(`Personal ratio ${ratio}`)).toBeVisible()
  }
)

it('updates number display when language changes while leaving ratio input strings untouched', async () => {
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
  const items = [
    { model: 'demo', channel_id: 11, authorized: true, ratio: '1.234567' },
  ]
  render(
    <>
      <ResellerUsageFacts usage={{ input_tokens: 12345.67 }} />
      <ResellerConfigEditor
        userID={3}
        admin={false}
        config={{ revision: 7, items }}
        candidates={items}
        onSuccess={() => undefined}
        onReload={async () => undefined}
      />
    </>,
    { wrapper: provider() }
  )
  expect(screen.getByText('Input Tokens: 12,345.67')).toBeVisible()
  i18next.addResourceBundle('fr', 'translation', {
    __reseller_locale_fixture__: 'fixture',
  })
  await act(async () => {
    await i18next.changeLanguage('fr')
  })
  expect(
    screen.getByText(`Input Tokens: ${formatNumber(12345.67, 'fr')}`, {
      normalizer: (value) => value,
    })
  ).toBeVisible()
  expect(screen.getByLabelText('Ratio for demo channel 11')).toHaveValue(
    '1.234567'
  )
})

it('reloads a 409 configuration only after discard confirmation and preserves the draft if reloading fails', async () => {
  const user = userEvent.setup()
  useAuthStore
    .getState()
    .auth.setUser({ id: 2, username: 'agent', role: 1, reseller_kind: 'agent' })
  const workspace = (revision: number, ratio: string) => ({
    user: {
      user_id: 3,
      username: 'customer',
      email: 'customer@example.invalid',
      activation: 'active',
      quota: 500000,
    },
    config: {
      revision,
      items: [{ model: 'demo', channel_id: 11, authorized: true, ratio }],
    },
    candidates: [
      { model: 'demo', channel_id: 11, authorized: true, ratio: '1' },
    ],
    history: [],
  })
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValue({ data: { success: true, data: workspace(7, '0.9') } })
  const put = vi.spyOn(api, 'put').mockRejectedValue(
    new AxiosError('Conflict', 'ERR_BAD_REQUEST', undefined, undefined, {
      status: 409,
      data: {
        success: false,
        code: 'RESELLER_REVISION_CONFLICT',
        data: { revision: 8 },
      },
      statusText: 'Conflict',
      headers: {},
      config: {} as never,
    })
  )
  render(<ResellerWorkspacePage userID={3} />, { wrapper: provider() })
  await user.click(
    await screen.findByRole('tab', { name: 'Model authorization and ratios' })
  )
  const ratio = screen.getByLabelText('Ratio for demo channel 11')
  await user.clear(ratio)
  await user.type(ratio, '0.7')
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  expect(
    await screen.findByText(
      'Configuration changed on the server (revision 8). Your draft is preserved.'
    )
  ).toBeVisible()
  expect(get).toHaveBeenCalledTimes(1)
  await user.click(screen.getByRole('button', { name: 'Reload configuration' }))
  const confirm = await screen.findByRole('alertdialog')
  expect(confirm).toHaveTextContent(
    'Reloading will discard your unsaved changes and load the latest configuration. Continue?'
  )
  await user.click(within(confirm).getByRole('button', { name: 'Cancel' }))
  expect(ratio).toHaveValue('0.7')
  expect(get).toHaveBeenCalledTimes(1)
  get.mockRejectedValueOnce(new Error('offline'))
  await user.click(screen.getByRole('button', { name: 'Reload configuration' }))
  await user.click(
    within(await screen.findByRole('alertdialog')).getByRole('button', {
      name: 'Reload configuration',
    })
  )
  expect(
    await screen.findByText(
      'Failed to reload configuration. Your draft is preserved.'
    )
  ).toBeVisible()
  expect(ratio).toHaveValue('0.7')
  get.mockResolvedValue({ data: { success: true, data: workspace(8, '0.8') } })
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Reload configuration',
    })
  )
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  expect(screen.getByLabelText('Ratio for demo channel 11')).toHaveValue('0.8')
  expect(screen.getByRole('button', { name: 'Save changes' })).toBeDisabled()
  await user.clear(screen.getByLabelText('Ratio for demo channel 11'))
  await user.type(screen.getByLabelText('Ratio for demo channel 11'), '0.6')
  put.mockResolvedValue({ data: { success: true, data: {} } })
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  expect(put).toHaveBeenLastCalledWith(
    '/api/reseller/self/customers/3/config',
    expect.objectContaining({ expected_revision: 8 })
  )
})

it('shows a preview revision conflict without opening an authorization confirmation', async () => {
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: false,
      code: 'RESELLER_REVISION_CONFLICT',
      data: { revision: 12 },
    },
  })
  const items = [
    { model: 'demo', channel_id: 11, authorized: true, ratio: '1' },
  ]
  render(
    <ResellerConfigEditor
      userID={2}
      admin
      config={{ revision: 7, items }}
      candidates={items}
      onSuccess={() => undefined}
      onReload={async () => undefined}
    />,
    { wrapper: provider() }
  )
  await userEvent.click(
    screen.getByRole('checkbox', { name: 'Authorize demo channel 11' })
  )
  await userEvent.click(screen.getByRole('button', { name: 'Save changes' }))
  expect(
    await screen.findByText(
      'Configuration changed on the server (revision 12). Your draft is preserved.'
    )
  ).toBeVisible()
  expect(post).toHaveBeenCalledWith(
    '/api/reseller/admin/agents/2/config/preview',
    expect.objectContaining({ expected_revision: 7 })
  )
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  expect(
    screen.getByRole('checkbox', { name: 'Authorize demo channel 11' })
  ).not.toBeChecked()
})

it('does not offer to discard a draft for an unrelated 409 conflict', async () => {
  vi.spyOn(api, 'put').mockRejectedValue(
    new AxiosError('Conflict', 'ERR_BAD_REQUEST', undefined, undefined, {
      status: 409,
      data: { success: false, code: 'RESELLER_OPERATION_CONFLICT' },
      statusText: 'Conflict',
      headers: {},
      config: {} as never,
    })
  )
  const items = [
    { model: 'demo', channel_id: 11, authorized: true, ratio: '1' },
  ]
  render(
    <ResellerConfigEditor
      userID={3}
      admin={false}
      config={{ revision: 7, items }}
      candidates={items}
      onSuccess={() => undefined}
      onReload={async () => undefined}
    />,
    { wrapper: provider() }
  )
  await userEvent.type(screen.getByLabelText('Ratio for demo channel 11'), '.1')
  await userEvent.click(screen.getByRole('button', { name: 'Save changes' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Save failed or configuration changed. Refresh and compare before saving again.'
  )
  expect(
    screen.queryByRole('button', { name: 'Reload configuration' })
  ).not.toBeInTheDocument()
  expect(screen.getByLabelText('Ratio for demo channel 11')).toHaveValue('1.1')
})

it('formats the authorization impact count without changing its numeric meaning', async () => {
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: { affected_customers: 12345 } },
  })
  const items = [
    { model: 'demo', channel_id: 11, authorized: true, ratio: '1' },
  ]
  render(
    <ResellerConfigEditor
      userID={2}
      admin
      config={{ revision: 7, items }}
      candidates={items}
      onSuccess={() => undefined}
      onReload={async () => undefined}
    />,
    { wrapper: provider() }
  )
  await userEvent.click(
    screen.getByRole('checkbox', { name: 'Authorize demo channel 11' })
  )
  await userEvent.click(screen.getByRole('button', { name: 'Save changes' }))
  expect(await screen.findByRole('alertdialog')).toHaveTextContent(
    '12,345 customers will be affected.'
  )
})

it.each([
  ['0.9', 'Personal price 0.9× (discount)'],
  ['1.3', 'Personal price 1.3× (markup)'],
])(
  'describes the meaning of a personal price ratio %s',
  async (ratio, label) => {
    useAuthStore.getState().auth.setUser({
      id: 3,
      username: 'customer',
      role: 1,
      reseller_kind: 'agent_customer',
    })
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: [{ model: 'demo', min: ratio, max: ratio }],
      },
    })
    render(<PersonalRatioBadge model='demo' />, { wrapper: provider() })
    expect(await screen.findByText(label)).toBeVisible()
  }
)

it('uses the server cursor to page quota adjustments even when a page is short', async () => {
  useAuthStore
    .getState()
    .auth.setUser({ id: 2, username: 'agent', role: 1, reseller_kind: 'agent' })
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValueOnce({
      data: {
        success: true,
        data: {
          items: [
            {
              id: 21,
              occurred_at: 1,
              mode: 'add',
              quota: 500000,
              before: 0,
              after: 500000,
              reason: 'Confirmed receipt',
            },
          ],
          next_cursor: 21,
        },
      },
    })
    .mockResolvedValueOnce({
      data: { success: true, data: { items: [], next_cursor: 0 } },
    })
  render(<ResellerAdjustments userID={3} admin={false} />, {
    wrapper: provider(),
  })
  expect(await screen.findByText('Confirmed receipt')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Next page' })).toBeEnabled()
  await userEvent.click(screen.getByRole('button', { name: 'Next page' }))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Next page' })).toBeDisabled()
  )
  expect(get).toHaveBeenLastCalledWith(
    '/api/reseller/self/customers/3/quota-adjustments?after=21'
  )
})

it('loads the next personal history page with the cross-version cursor', async () => {
  useAuthStore.getState().auth.setUser({
    id: 3,
    username: 'customer',
    role: 1,
    reseller_kind: 'agent_customer',
  })
  const entries = Array.from({ length: 50 }, (_, index) => ({
    pricing_version: 2,
    revision: 60 - index,
    cursor: `2000:2:${60 - index}`,
    created_at: 2000,
    models: [],
  }))
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValueOnce({ data: { success: true, data: entries } })
    .mockResolvedValueOnce({
      data: {
        success: true,
        data: [
          {
            pricing_version: 0,
            revision: 50,
            cursor: '1000:0:50',
            created_at: 1000,
            models: [{ model: 'legacy-A', min: '0.8', max: '0.8' }],
          },
        ],
      },
    })
  render(<PersonalPricingHistory />, { wrapper: provider() })
  await userEvent.click(
    await screen.findByRole('button', { name: 'Load more' })
  )
  await waitFor(() =>
    expect(get).toHaveBeenCalledWith(
      '/api/reseller/personal/history?cursor=2000%3A2%3A11'
    )
  )
  expect(await screen.findByText(/legacy-A/)).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Load more' })
  ).not.toBeInTheDocument()
})
