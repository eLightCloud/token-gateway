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
import { resellerPath, resellerRequest, type ResellerUser } from './api'

type ResellerAgentControlsProps = {
  user: ResellerUser
  onChanged: () => void
  detail?: boolean
}

export function ResellerAgentControls(props: ResellerAgentControlsProps) {
  const { t } = useTranslation()
  const [confirmOpen, setConfirmOpen] = useState(false)
  const { user } = props
  const enabled = user.account?.enabled
  const path = `${resellerPath(true)}/${user.user_id}`
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
          {enabled ? t('Disable') : t('Activate account')}
        </Button>
      )}
      {user.activation === 'pending' ? (
        <ResellerAgentActivationDialog
          user={user}
          onChanged={props.onChanged}
          triggerLabel={
            props.detail ? t('Reset Password') : t('Send account information')
          }
        />
      ) : (
        <ResellerAgentPasswordReset
          accountPath={path}
          disabled={user.platform_disabled}
        />
      )}
      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={enabled ? t('Disable account') : t('Activate account')}
        desc={
          enabled
            ? t(
                'Disabling this reseller stops new requests for all its customers. Existing billing continues.'
              )
            : t(
                'Restoring this reseller keeps each customer’s independent restrictions in effect.'
              )
        }
        destructive={enabled}
        isLoading={toggle.isPending}
        handleConfirm={() => toggle.mutate(!enabled)}
      />
    </>
  )
}
