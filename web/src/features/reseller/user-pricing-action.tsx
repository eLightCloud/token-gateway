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
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Dialog } from '@/components/dialog'
import { DropdownMenuItem } from '@/components/ui/dropdown-menu'
import { useAuthStore } from '@/stores/auth-store'

import { resellerKey, resellerRequest, type Workspace } from './api'
import { ResellerConfigEditor } from './config-editor'
import { ResellerQueryStatus } from './shared'

export function UserPersonalPricingAction({ userID }: { userID: number }) {
  const { t } = useTranslation()
  const viewer = useAuthStore((s) => s.auth.user?.id)
  const [open, setOpen] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [discardOpen, setDiscardOpen] = useState(false)
  const query = useQuery({
    meta: { errorToast: false },
    queryKey: resellerKey(viewer, 'ordinary-pricing', userID),
    queryFn: () =>
      resellerRequest<Workspace>(`/api/reseller/admin/agents/${userID}`),
    enabled: open,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  })
  return (
    <>
      <DropdownMenuItem
        onSelect={(event) => {
          event.preventDefault()
          setOpen(true)
        }}
      >
        {t('Personal ratio')}
      </DropdownMenuItem>
      <Dialog
        open={open}
        onOpenChange={(next) => {
          if (!next && dirty) {
            setDiscardOpen(true)
            return
          }
          setOpen(next)
        }}
        title={t('Personal ratio')}
      >
        <ResellerQueryStatus
          pending={query.isPending}
          error={query.isError}
          retry={() => void query.refetch()}
        />
        {query.data && (
          <ResellerConfigEditor
            key={`${userID}:${query.data.config.revision}`}
            userID={userID}
            admin
            authorization={false}
            onDirtyChange={setDirty}
            config={query.data.config}
            candidates={query.data.candidates}
            onSuccess={() => void query.refetch()}
            onReload={async () => {
              const result = await query.refetch()
              if (result.isError) throw result.error
            }}
          />
        )}
      </Dialog>
      <ConfirmDialog
        open={discardOpen}
        onOpenChange={setDiscardOpen}
        title={t('Unsaved changes')}
        desc={t('You have unsaved changes. Are you sure you want to leave?')}
        confirmText={t('Leave')}
        cancelBtnText={t('Stay')}
        destructive
        handleConfirm={() => {
          setDiscardOpen(false)
          setDirty(false)
          setOpen(false)
        }}
      />
    </>
  )
}
