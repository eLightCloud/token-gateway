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
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'
import { handleServerError } from '@/lib/handle-server-error'

import { ResellerAgentActivationDialog } from './agent-activation-dialog'
import { ResellerAgentPasswordReset } from './agent-password-reset'
import { hasResellerAgentPassword } from './agent-status'
import { resellerPath, resellerRequest, type ResellerUser } from './api'

type ResellerAgentControlsProps = {
  user: ResellerUser
  onChanged: () => void
  customer?: boolean
  sending?: boolean
}

export function ResellerAgentControls(props: ResellerAgentControlsProps) {
  const { t } = useTranslation()
  const [confirmOpen, setConfirmOpen] = useState(false)
  const { user } = props
  const enabled = props.customer ? !user.agent_disabled : user.account?.enabled
  const path = `${resellerPath(!props.customer)}/${user.user_id}`
  const action = enabled ? t('Disable') : t('Activate account')
  const title = enabled ? t('Disable account') : action
  const description = props.customer
    ? t(
        'This changes only the agent restriction. Platform restrictions remain in effect.'
      )
    : t(
        enabled
          ? 'Disabling this reseller stops new requests for all its customers. Existing billing continues.'
          : 'Restoring this reseller keeps each customer’s independent restrictions in effect.'
      )
  const toggle = useMutation({
    meta: { errorToast: false },
    mutationFn: (next: boolean) =>
      resellerRequest(`${path}/enabled`, 'put', { enabled: next }),
    onError: (error) => handleServerError(error, t('Operation failed')),
    onSuccess: () => {
      setConfirmOpen(false)
      props.onChanged()
    },
  })
  return (
    <>
      {enabled !== undefined && (
        <Button
          variant='outline'
          size='sm'
          disabled={toggle.isPending || (!enabled && user.platform_disabled)}
          onClick={() => setConfirmOpen(true)}
        >
          {action}
        </Button>
      )}
      {user.activation === 'pending' && (
        <ResellerAgentActivationDialog
          user={user}
          onChanged={props.onChanged}
          customer={props.customer}
          sending={props.sending}
        />
      )}
      {hasResellerAgentPassword(user.activation) && (
        <ResellerAgentPasswordReset
          accountPath={path}
          disabled={user.platform_disabled}
        />
      )}
      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={title}
        desc={description}
        destructive={enabled}
        isLoading={toggle.isPending}
        handleConfirm={() => toggle.mutate(!enabled)}
      />
    </>
  )
}
