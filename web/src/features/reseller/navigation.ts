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
import { ReceiptText, ShieldCheck, Tags, UserRound, Users } from 'lucide-react'

import type { NavGroup } from '@/components/layout/types'

import { observationMenu, isObservationPath } from './observation/routes'

export function guardResellerRoute(kind: string, pathname: string) {
  pathname = pathname.replace(/\/$/, '')
  if (
    kind === 'agent' &&
    !pathname.startsWith('/reseller/customers') &&
    !isObservationPath(pathname) &&
    pathname !== '/reseller/account' &&
    pathname !== '/reseller/billing' &&
    pathname !== '/reseller/procurement' &&
    pathname !== '/reseller/discounts' &&
    pathname !== '/profile' &&
    pathname !== '/security'
  ) {
    throw redirect({ to: '/reseller/customers', replace: true })
  }
}

export function resellerSidebar(
  kind: string | undefined,
  groups: NavGroup[],
  t: (key: string) => string
): NavGroup[] {
  if (kind === 'agent') {
    return [
      observationMenu(groups, t),
      {
        id: 'reseller',
        title: t('Reseller business'),
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
            title: t('My procurement ratios'),
            url: '/reseller/discounts',
            icon: Tags,
          },
        ],
      },
      {
        id: 'reseller-account',
        title: t('My account'),
        items: [
          {
            title: t('Profile'),
            url: '/profile',
            icon: UserRound,
          },
          {
            title: t('Security & Access'),
            url: '/security',
            icon: ShieldCheck,
          },
        ],
      },
    ]
  }
  return groups
}
