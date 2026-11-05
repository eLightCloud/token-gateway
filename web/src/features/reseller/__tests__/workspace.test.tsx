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
import { toIntlLocale } from '@/i18n/languages'
import { api } from '@/lib/api'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { formatNumber } from '@/lib/format'
import { useAuthStore } from '@/stores/auth-store'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

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

it('describes restoring a reseller without implying customer restrictions are removed', async () => {
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
  await userEvent.click(await screen.findByRole('button', { name: 'Enable' }))
  expect(await screen.findByRole('alertdialog')).toHaveTextContent(
    'Restoring this reseller keeps each customer’s independent restrictions in effect.'
  )
  expect(screen.getByRole('alertdialog')).not.toHaveTextContent(
    'Disabling this reseller'
  )
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
  useAuthStore
    .getState()
    .auth.setUser({
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
