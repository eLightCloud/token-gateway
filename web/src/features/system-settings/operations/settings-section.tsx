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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { handleServerError } from '@/lib/handle-server-error'

import {
  SettingsForm,
  SettingsSwitchField,
} from '../components/settings-form-layout'
import { SettingsSection } from '../components/settings-section'
import {
  type OperationsDelivery,
  type OperationsResponse,
  getOperations,
  operationsQueryKey,
  retryOperationsDelivery,
  saveOperations,
} from './api'

const schema = z.object({
  daily_enabled: z.boolean(),
  emergency_enabled: z.boolean(),
  preview_only: z.boolean(),
  recipients: z.string().max(2048),
})

export function OperationsSettingsSection() {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: operationsQueryKey,
    queryFn: getOperations,
    staleTime: 30_000,
    refetchOnWindowFocus: false,
    meta: { errorToast: false },
  })
  if (query.isPending) {
    return <LoadingState message={t('Loading settings...')} />
  }
  if (!query.data) {
    return (
      <ErrorState
        title={t('Failed to load settings')}
        onRetry={() => void query.refetch()}
      />
    )
  }
  return <OperationsForm data={query.data} />
}

function OperationsForm(props: { data: OperationsResponse }) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const form = useForm<z.infer<typeof schema>>({
    resolver: zodResolver(schema),
    defaultValues: props.data.settings,
  })
  const [selected, setSelected] = useState<OperationsDelivery | null>(null)
  const [retry, setRetry] = useState<OperationsDelivery | null>(null)
  const save = useMutation({
    mutationFn: saveOperations,
    onSuccess: async (settings) => {
      form.reset(settings)
      await client.invalidateQueries({ queryKey: operationsQueryKey })
    },
    onError: (error) => handleServerError(error),
  })
  const resend = useMutation({
    mutationFn: retryOperationsDelivery,
    onSuccess: async () => {
      setRetry(null)
      await client.invalidateQueries({ queryKey: operationsQueryKey })
    },
    onError: (error) => handleServerError(error),
  })
  const statusLabels: Record<string, string> = {
    pending: t('Notification status: pending'),
    preview: t('Notification status: preview'),
    sending: t('Notification status: sending'),
    accepted: t('Notification status: accepted'),
    failed: t('Notification status: failed'),
    unknown: t('Notification status: unknown'),
    skipped: t('Notification status: skipped'),
  }
  const preview = selected
    ? new DOMParser().parseFromString(
        selected.body.replaceAll(/<\/(p|h2)>/g, '\n'),
        'text/html'
      ).body.textContent
    : ''
  return (
    <SettingsSection title={t('Operations notifications')}>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Daily summaries start after 06:00 Beijing time and yield to business traffic. Emergency email is independent of daily summaries.'
        )}
      </p>
      {!props.data.runtime.mail_ready && (
        <p role='status' className='text-destructive text-sm'>
          {t(
            'Email delivery is unavailable. Check the saved email configuration.'
          )}
        </p>
      )}
      {props.data.runtime.paused && (
        <p role='status' className='text-muted-foreground text-sm'>
          {t(
            'Daily processing is paused while resource usage is high or samples are unavailable.'
          )}
        </p>
      )}
      <Form {...form}>
        <SettingsForm
          onSubmit={form.handleSubmit((values) => save.mutate(values))}
        >
          <FormField
            control={form.control}
            name='daily_enabled'
            render={({ field }) => (
              <SettingsSwitchField
                controlId='ops-daily'
                label={t('Enable daily summaries')}
                checked={field.value}
                onCheckedChange={field.onChange}
                disabled={save.isPending}
              />
            )}
          />
          <FormField
            control={form.control}
            name='preview_only'
            render={({ field }) => (
              <SettingsSwitchField
                controlId='ops-preview'
                label={t('Preview daily summaries without sending')}
                checked={field.value}
                onCheckedChange={field.onChange}
                disabled={save.isPending}
              />
            )}
          />
          <FormField
            control={form.control}
            name='emergency_enabled'
            render={({ field }) => (
              <SettingsSwitchField
                controlId='ops-emergency'
                label={t('Enable administrator emergency email')}
                description={t(
                  'Disk 90%, CPU 80%, memory 70%. Alerts do not change request protection thresholds.'
                )}
                checked={field.value}
                onCheckedChange={field.onChange}
                disabled={save.isPending}
              />
            )}
          />
          <FormField
            control={form.control}
            name='recipients'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Administrator email addresses')}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    placeholder='admin@example.com;ops@example.com'
                    disabled={save.isPending}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <Button
            type='submit'
            disabled={save.isPending || !form.formState.isDirty}
          >
            {t('Save')}
          </Button>
        </SettingsForm>
      </Form>
      <div className='flex items-center justify-between gap-3'>
        <h4 className='text-sm font-medium'>
          {t('Recent notification deliveries')}
        </h4>
        <Button
          variant='outline'
          size='sm'
          onClick={() =>
            void client.invalidateQueries({ queryKey: operationsQueryKey })
          }
        >
          {t('Refresh')}
        </Button>
      </div>
      <StaticDataTable tableClassName='min-w-[600px]'>
        <TableHeader>
          <TableRow>
            <TableHead>{t('Date')}</TableHead>
            <TableHead>{t('Recipient')}</TableHead>
            <TableHead>{t('Status')}</TableHead>
            <TableHead>{t('Actions')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {props.data.deliveries.length === 0 && (
            <TableRow>
              <TableCell colSpan={4}>
                {t('No notification deliveries yet.')}
              </TableCell>
            </TableRow>
          )}
          {props.data.deliveries.map((delivery) => (
            <TableRow key={delivery.key}>
              <TableCell>{delivery.day}</TableCell>
              <TableCell className='max-w-56 truncate'>
                {delivery.recipient || t('Unavailable')}
              </TableCell>
              <TableCell>
                {statusLabels[delivery.status] ?? t('Unavailable')}
                {delivery.reason && (
                  <p className='text-muted-foreground text-xs'>
                    {delivery.reason}
                  </p>
                )}
              </TableCell>
              <TableCell>
                <div className='flex gap-2'>
                  <Button
                    variant='outline'
                    size='sm'
                    onClick={() => setSelected(delivery)}
                  >
                    {t('Preview')}
                  </Button>
                  {delivery.kind !== 'emergency' &&
                    ['failed', 'unknown', 'preview'].includes(
                      delivery.status
                    ) && (
                      <Button
                        variant='outline'
                        size='sm'
                        onClick={() => setRetry(delivery)}
                        disabled={resend.isPending}
                      >
                        {t('Retry')}
                      </Button>
                    )}
                </div>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </StaticDataTable>
      <Dialog
        open={selected !== null}
        onOpenChange={(open) => {
          if (!open) setSelected(null)
        }}
        title={selected?.subject ?? t('Preview')}
      >
        <pre className='text-sm break-words whitespace-pre-wrap'>{preview}</pre>
      </Dialog>
      <ConfirmDialog
        open={retry !== null}
        onOpenChange={(open) => {
          if (!open) setRetry(null)
        }}
        title={t('Retry notification')}
        desc={t(
          'An unknown delivery may already have reached the recipient. Retry only after checking. Preview mode never sends daily email.'
        )}
        isLoading={resend.isPending}
        handleConfirm={() => {
          if (retry) resend.mutate(retry.key)
        }}
      />
    </SettingsSection>
  )
}
