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
import { RealtimePricingEditor } from '@/features/realtime-pricing/editor'
import { useAuthStore } from '@/stores/auth-store'

import { resellerKey, resellerRequest, type Workspace } from './api'
import { ResellerConfigEditor } from './config-editor'
import { ResellerQueryStatus } from './shared'

export function UserPersonalPricingAction(props: { onOpen: () => void }) {
  const { t } = useTranslation()
  return (
    <DropdownMenuItem onSelect={props.onOpen}>
      {t('Personal ratio')}
    </DropdownMenuItem>
  )
}

// Keep the dialog outside the menu popup: closing the menu must not unmount
// an unsaved pricing draft or its discard confirmation.
export function UserPersonalPricingDialog(props: {
  userID: number
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const userID = props.userID
  const { t } = useTranslation()
  const viewer = useAuthStore((s) => s.auth.user?.id)
  const [legacyDirty, setLegacyDirty] = useState(false)
  const [realtimeDirty, setRealtimeDirty] = useState(false)
  const dirty = legacyDirty || realtimeDirty
  const [discardOpen, setDiscardOpen] = useState(false)
  const query = useQuery({
    meta: { errorToast: false },
    queryKey: resellerKey(viewer, 'ordinary-pricing', userID),
    queryFn: () =>
      resellerRequest<Workspace>(`/api/reseller/admin/agents/${userID}`),
    enabled: props.open,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  })
  return (
    <>
      <Dialog
        open={props.open}
        onOpenChange={(next) => {
          if (!next && dirty) {
            setDiscardOpen(true)
            return
          }
          props.onOpenChange(next)
        }}
        title={t('Personal ratio')}
      >
        <ResellerQueryStatus
          pending={query.isPending}
          error={query.isError}
          retry={() => void query.refetch()}
        />
        {query.data && query.data.pricing_phase !== 'active' && (
          <ResellerConfigEditor
            key={`${userID}:${query.data.config.revision}`}
            userID={userID}
            admin
            authorization={false}
            onDirtyChange={setLegacyDirty}
            config={query.data.config}
            candidates={query.data.candidates}
            onSuccess={() => void query.refetch()}
            onReload={async () => {
              const result = await query.refetch()
              if (result.isError) throw result.error
            }}
          />
        )}
        <RealtimePricingEditor
          subject='user'
          subjectID={userID}
          onDirtyChange={setRealtimeDirty}
        />
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
          setLegacyDirty(false)
          setRealtimeDirty(false)
          props.onOpenChange(false)
        }}
      />
    </>
  )
}
