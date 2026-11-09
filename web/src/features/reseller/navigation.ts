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
import { redirect } from '@tanstack/react-router'
import type { TFunction } from 'i18next'
import { ReceiptText, ShieldCheck, UserRound, Users } from 'lucide-react'

import type { NavGroup } from '@/components/layout/types'

export function guardResellerRoute(kind: string, pathname: string) {
  pathname = pathname.replace(/\/$/, '')
  if (
    kind === 'agent' &&
    !pathname.startsWith('/reseller/customers') &&
    pathname !== '/reseller/account' &&
    pathname !== '/reseller/billing' &&
    pathname !== '/reseller/procurement' &&
    pathname !== '/security'
  ) {
    throw redirect({ to: '/reseller/customers', replace: true })
  }
  if (kind === 'agent_customer' && pathname.startsWith('/usage-logs/audit')) {
    throw redirect({
      to: '/usage-logs/$section',
      params: { section: 'common' },
      replace: true,
    })
  }
  if (
    kind === 'agent_customer' &&
    ![
      '/dashboard',
      '/keys',
      '/usage-logs',
      '/wallet',
      '/security',
      '/pricing',
    ].some((path) => pathname === path || pathname.startsWith(`${path}/`))
  ) {
    throw redirect({ to: '/wallet', replace: true })
  }
}

export function resellerSidebar(
  kind: string | undefined,
  groups: NavGroup[],
  t: TFunction
): NavGroup[] {
  if (kind === 'agent') {
    return [
      {
        id: 'reseller',
        title: t('Reseller'),
        items: [
          {
            title: t('Customer management'),
            url: '/reseller/customers',
            icon: Users,
          },
          {
            title: t('Customer billing'),
            url: '/reseller/billing',
            icon: ReceiptText,
          },
          {
            title: t('Procurement ledger'),
            url: '/reseller/procurement',
            icon: ReceiptText,
          },
          {
            title: t('Account details'),
            url: '/reseller/account',
            icon: UserRound,
          },
          { title: t('Account Security'), url: '/security', icon: ShieldCheck },
        ],
      },
    ]
  }
  if (kind === 'agent_customer') {
    return groups
      .filter((group) => group.id !== 'admin' && group.id !== 'chat')
      .map((group) => ({
        ...group,
        items: group.items.filter(
          (item) =>
            'url' in item &&
            item.url &&
            !item.url.startsWith('/usage-logs/audit') &&
            [
              '/dashboard',
              '/keys',
              '/usage-logs',
              '/wallet',
              '/security',
              '/pricing',
            ].some(
              (path) => item.url === path || item.url?.startsWith(`${path}/`)
            )
        ),
      }))
      .filter((group) => group.items.length > 0)
  }
  return groups
}
