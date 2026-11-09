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
import { useMutation } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Field, FieldError, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { handleServerError } from '@/lib/handle-server-error'

import { sendResellerActivationEmail } from './activation-mail'
import type { ResellerUser } from './api'
import {
  pendingResellerEmailSchema,
  type PendingResellerEmail,
} from './lib/profile-schema'

export function ResellerAgentActivationDialog(props: {
  user: ResellerUser
  onChanged: () => void
  triggerLabel: string
}) {
  const { t } = useTranslation()
  const id = useId()
  const [open, setOpen] = useState(false)
  const form = useForm<PendingResellerEmail>({
    resolver: zodResolver(pendingResellerEmailSchema),
    defaultValues: { email: '' },
  })
  const error = form.formState.errors.email?.message
  const send = useMutation({
    meta: { errorToast: false },
    retry: false,
    mutationFn: (values: PendingResellerEmail) =>
      sendResellerActivationEmail(true, props.user.user_id, values.email),
    onSuccess: () => {
      toast.success(t('Activation email submitted to the mail server.'))
      setOpen(false)
      form.reset()
      props.onChanged()
    },
    onError: (error) =>
      handleServerError(error, t('Failed to send activation email')),
  })

  return (
    <>
      <Button
        variant='outline'
        size='sm'
        disabled={props.user.platform_disabled}
        onClick={() => setOpen(true)}
      >
        {props.triggerLabel}
      </Button>
      <Dialog
        open={open}
        onOpenChange={(nextOpen) => {
          if (send.isPending) return
          setOpen(nextOpen)
          if (!nextOpen) form.reset()
        }}
        title={t('Set Password')}
        description={t(
          'An activation email lets the account holder set a password and configure MFA.'
        )}
      >
        <form
          noValidate
          className='space-y-4'
          onSubmit={form.handleSubmit((values) => {
            if (!send.isPending) send.mutate(values)
          })}
        >
          <Field data-invalid={!!error}>
            <FieldLabel htmlFor={id}>{t('Correct pending email')}</FieldLabel>
            <Input
              {...form.register('email')}
              id={id}
              type='email'
              placeholder={props.user.email}
              disabled={send.isPending}
              aria-invalid={!!error}
              aria-describedby={error ? `${id}-error` : undefined}
            />
            {error && <FieldError id={`${id}-error`}>{t(error)}</FieldError>}
          </Field>
          <Button type='submit' disabled={send.isPending}>
            {send.isPending ? t('Sending...') : t('Send activation email')}
          </Button>
        </form>
      </Dialog>
    </>
  )
}
