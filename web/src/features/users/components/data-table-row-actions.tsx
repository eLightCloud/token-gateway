import { getRouteApi } from '@tanstack/react-router'
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
import type { Row } from '@tanstack/react-table'
import {
  Pencil,
  Trash2,
  Power,
  PowerOff,
  ArrowUp,
  ArrowDown,
  KeyRound,
  ShieldAlert,
  Link2,
  CreditCard,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { DataTableRowActionMenu } from '@/components/data-table/core/row-action-menu'
import { Button } from '@/components/ui/button'
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
} from '@/components/ui/dropdown-menu'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { SecurityRecoveryDialog } from '@/features/security-completion/recovery-dialog'
import { UserSubscriptionsDialog } from '@/features/subscriptions/components/dialogs/user-subscriptions-dialog'
import { hasPermission } from '@/lib/admin-permissions'
import { handleServerError } from '@/lib/handle-server-error'
import { useAuthStore } from '@/stores/auth-store'

import { manageUser } from '../api'
import {
  USER_STATUS,
  USER_ROLE,
  ERROR_MESSAGES,
  isUserDeleted,
} from '../constants'
import { getUserActionMessage } from '../lib'
import type { User, ManageUserAction } from '../types'
import { UserBindingDialog } from './dialogs/user-binding-dialog'
import { useUsers } from './users-provider'

interface DataTableRowActionsProps {
  row: Row<User>
}

export function DataTableRowActions({ row }: DataTableRowActionsProps) {
  const { t } = useTranslation()
  const user = row.original
  const operator = useAuthStore((s) => s.auth.user)
  const canRecoverSecurity =
    hasPermission(operator, 'account_security', 'recover') &&
    operator?.id !== user.id &&
    (operator?.role === USER_ROLE.ROOT || (operator?.role ?? 0) > user.role)
  const { setOpen, setCurrentRow, triggerRefresh } = useUsers()
  const [resetPasskeyOpen, setResetPasskeyOpen] = useState(false)
  const [resetTwoFAOpen, setResetTwoFAOpen] = useState(false)
  const [emailChangeOpen, setEmailChangeOpen] = useState(false)
  const [bindingDialogOpen, setBindingDialogOpen] = useState(false)
  const [subscriptionsDialogOpen, setSubscriptionsDialogOpen] = useState(false)
  const navigate = getRouteApi('/_authenticated/users/').useNavigate()

  const handleEdit = () => {
    setCurrentRow(user)
    setOpen('update')
  }

  const handleDelete = () => {
    setCurrentRow(user)
    setOpen('delete')
  }

  const handleManage = async (action: Exclude<ManageUserAction, 'delete'>) => {
    try {
      const result = await manageUser(user.id, action)
      if (result.success) {
        toast.success(t(getUserActionMessage(action)))
        triggerRefresh()
      } else {
        handleServerError(result, t('Failed to {{action}} user', { action }))
      }
    } catch (error) {
      handleServerError(error, t(ERROR_MESSAGES.UNEXPECTED))
    }
  }

  const isDisabled = user.status === USER_STATUS.DISABLED
  const isAdmin = user.role >= USER_ROLE.ADMIN
  const isRoot = user.role === USER_ROLE.ROOT

  if (isUserDeleted(user)) {
    return null
  }

  return (
    <div className='-ml-1.5 flex items-center gap-1'>
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant='ghost'
              size='icon-sm'
              onClick={handleEdit}
              aria-label={t('Edit')}
            />
          }
        >
          <Pencil />
        </TooltipTrigger>
        <TooltipContent>{t('Edit')}</TooltipContent>
      </Tooltip>

      <DataTableRowActionMenu
        ariaLabel={t('Open menu')}
        contentClassName='w-48'
      >
        {!user.reseller_kind && (
          <DropdownMenuItem
            onSelect={() =>
              void navigate({
                search: (search) => ({ ...search, pricingUserId: user.id }),
              })
            }
          >
            {t('Personal ratio')}
          </DropdownMenuItem>
        )}
        {isDisabled ? (
          <DropdownMenuItem onClick={() => handleManage('enable')}>
            {t('Enable')}
            <DropdownMenuShortcut>
              <Power size={16} />
            </DropdownMenuShortcut>
          </DropdownMenuItem>
        ) : (
          <DropdownMenuItem
            onClick={() => handleManage('disable')}
            disabled={isRoot}
          >
            {t('Disable')}
            <DropdownMenuShortcut>
              <PowerOff size={16} />
            </DropdownMenuShortcut>
          </DropdownMenuItem>
        )}

        {!user.reseller_kind && isAdmin && !isRoot && (
          <DropdownMenuItem onClick={() => handleManage('demote')}>
            {t('Demote')}
            <DropdownMenuShortcut>
              <ArrowDown size={16} />
            </DropdownMenuShortcut>
          </DropdownMenuItem>
        )}

        {!user.reseller_kind && !isAdmin && (
          <DropdownMenuItem onClick={() => handleManage('promote')}>
            {t('Promote')}
            <DropdownMenuShortcut>
              <ArrowUp size={16} />
            </DropdownMenuShortcut>
          </DropdownMenuItem>
        )}

        <DropdownMenuItem
          onSelect={(event) => {
            event.preventDefault()
            setBindingDialogOpen(true)
          }}
        >
          {t('Manage Bindings')}
          <DropdownMenuShortcut>
            <Link2 size={16} />
          </DropdownMenuShortcut>
        </DropdownMenuItem>

        {!user.reseller_kind && (
          <DropdownMenuItem
            onSelect={(event) => {
              event.preventDefault()
              setSubscriptionsDialogOpen(true)
            }}
          >
            {t('Manage Subscriptions')}
            <DropdownMenuShortcut>
              <CreditCard size={16} />
            </DropdownMenuShortcut>
          </DropdownMenuItem>
        )}

        <DropdownMenuSeparator />

        <DropdownMenuItem
          onSelect={(event) => {
            event.preventDefault()
            setResetPasskeyOpen(true)
          }}
          disabled={!canRecoverSecurity}
        >
          {t('Reset Passkey')}
          <DropdownMenuShortcut>
            <KeyRound size={16} />
          </DropdownMenuShortcut>
        </DropdownMenuItem>

        <DropdownMenuItem
          onSelect={(event) => {
            event.preventDefault()
            setEmailChangeOpen(true)
          }}
          disabled={!canRecoverSecurity}
        >
          {t('Request email change')}
        </DropdownMenuItem>
        <DropdownMenuItem
          onSelect={(event) => {
            event.preventDefault()
            setResetTwoFAOpen(true)
          }}
          disabled={!canRecoverSecurity}
        >
          {t('Reset 2FA')}
          <DropdownMenuShortcut>
            <ShieldAlert size={16} />
          </DropdownMenuShortcut>
        </DropdownMenuItem>

        <DropdownMenuSeparator />

        {!user.reseller_kind && (
          <DropdownMenuItem
            onClick={handleDelete}
            className='text-destructive focus:text-destructive'
            disabled={isRoot}
          >
            {t('Delete')}
            <DropdownMenuShortcut>
              <Trash2 size={16} />
            </DropdownMenuShortcut>
          </DropdownMenuItem>
        )}
      </DataTableRowActionMenu>

      <SecurityRecoveryDialog
        key={`passkey-${user.id}`}
        passkeyRecovery
        open={resetPasskeyOpen}
        onOpenChange={setResetPasskeyOpen}
        userID={user.id}
        onSuccess={triggerRefresh}
      />

      <SecurityRecoveryDialog
        key={`email-${user.id}`}
        emailChange
        open={emailChangeOpen}
        onOpenChange={setEmailChangeOpen}
        userID={user.id}
        onSuccess={triggerRefresh}
      />
      <SecurityRecoveryDialog
        key={user.id}
        open={resetTwoFAOpen}
        onOpenChange={setResetTwoFAOpen}
        userID={user.id}
        onSuccess={triggerRefresh}
      />

      <UserBindingDialog
        open={bindingDialogOpen}
        onOpenChange={setBindingDialogOpen}
        userId={user.id}
        onUnbindSuccess={triggerRefresh}
      />

      <UserSubscriptionsDialog
        open={subscriptionsDialogOpen}
        onOpenChange={setSubscriptionsDialogOpen}
        user={{ id: user.id, username: user.username }}
        onSuccess={triggerRefresh}
      />
    </div>
  )
}
