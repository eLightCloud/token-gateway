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
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardAction,
  CardContent,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Field,
  FieldError,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { EmailBindDialog } from '@/features/security/components/dialogs/email-bind-dialog'
import { handleServerError } from '@/lib/handle-server-error'

import { ResellerAgentPasswordReset } from './agent-password-reset'
import {
  hasResellerAgentPassword,
  isResellerAgentActivated,
} from './agent-status'
import {
  resellerOwnAccountPath,
  resellerRequest,
  type ResellerUser,
} from './api'
import {
  resellerProfileSchema,
  type ResellerProfileValues,
} from './lib/profile-schema'

export function ResellerAgentInformation(props: {
  user: ResellerUser
  onSaved?: () => void
}) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState(false)
  const [changingEmail, setChangingEmail] = useState(false)
  const user = props.user
  const enabled = isResellerAgentActivated(user)
  const fields = [
    { label: t('Username'), value: user.username },
    { label: t('Display name'), value: user.display_name },
    { label: t('Email'), value: user.email },
    { label: t('Mobile number'), value: user.phone },
    {
      label: t('Password'),
      value: hasResellerAgentPassword(user.activation)
        ? t('Configured')
        : t('Password not set'),
    },
    {
      label: t('Service status'),
      value: (
        <Badge variant={enabled ? 'secondary' : 'outline'}>
          {enabled ? t('Activated') : t('Disabled')}
        </Badge>
      ),
    },
  ]
  return (
    <Card className='mt-2'>
      <CardHeader>
        <CardTitle>{t('Account details')}</CardTitle>
        {props.onSaved && (
          <CardAction className='col-span-full col-start-1 row-span-1 row-start-2 flex flex-wrap gap-2 justify-self-start sm:col-span-1 sm:col-start-2 sm:row-start-1 sm:justify-self-end'>
            <Button
              variant='outline'
              size='sm'
              onClick={() => setEditing(true)}
            >
              {t('Edit profile')}
            </Button>
            <Button
              variant='outline'
              size='sm'
              onClick={() => setChangingEmail(true)}
            >
              {t('Change Email')}
            </Button>
            <ResellerAgentPasswordReset accountPath={resellerOwnAccountPath} />
          </CardAction>
        )}
      </CardHeader>
      <CardContent>
        <dl className='grid gap-x-6 gap-y-4 sm:grid-cols-2 lg:grid-cols-3'>
          {fields.map(({ label, value }) => (
            <div key={label} className='min-w-0 space-y-1'>
              <dt className='text-muted-foreground'>{label}</dt>
              <dd className='font-medium wrap-anywhere'>{value || '—'}</dd>
            </div>
          ))}
          {!props.onSaved && (
            <div className='space-y-1 border-t pt-4 sm:col-span-full'>
              <dt className='text-muted-foreground'>{t('Remark')}</dt>
              <dd className='wrap-anywhere whitespace-pre-wrap'>
                {user.remark || '—'}
              </dd>
            </div>
          )}
        </dl>
      </CardContent>
      {editing && (
        <ResellerProfileDialog
          user={user}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false)
            props.onSaved?.()
          }}
        />
      )}
      {props.onSaved && changingEmail && (
        <EmailBindDialog
          open={changingEmail}
          onOpenChange={setChangingEmail}
          currentEmail={user.email}
          requirePassword
          onSuccess={props.onSaved}
        />
      )}
    </Card>
  )
}

function ResellerProfileDialog(props: {
  user: ResellerUser
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const id = useId()
  const form = useForm<ResellerProfileValues>({
    resolver: zodResolver(resellerProfileSchema),
    defaultValues: {
      display_name: props.user.display_name,
      phone: props.user.phone ?? '',
    },
  })
  const { dirtyFields, errors, isDirty } = form.formState
  const update = useMutation({
    meta: { errorToast: false },
    mutationFn: (values: ResellerProfileValues) =>
      resellerRequest(resellerOwnAccountPath, 'put', {
        ...(dirtyFields.display_name && { display_name: values.display_name }),
        ...(dirtyFields.phone && { phone: values.phone }),
      }),
    onError: (error) => handleServerError(error, t('Failed to update profile')),
    onSuccess: () => {
      toast.success(t('Profile updated successfully'))
      props.onSaved()
    },
  })
  const fields = [
    {
      name: 'display_name',
      label: t('Display name'),
      autoComplete: 'nickname',
      type: 'text',
    },
    {
      name: 'phone',
      label: t('Mobile number'),
      autoComplete: 'tel',
      type: 'tel',
    },
  ] as const
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !update.isPending) props.onClose()
      }}
      title={t('Edit profile')}
    >
      <form
        noValidate
        onSubmit={form.handleSubmit((values) => update.mutate(values))}
      >
        <FieldGroup className='gap-4'>
          {fields.map((field) => {
            const fieldID = `${id}-${field.name}`
            const error = errors[field.name]?.message
            return (
              <Field key={field.name} data-invalid={!!error}>
                <FieldLabel htmlFor={fieldID}>{field.label}</FieldLabel>
                <Input
                  {...form.register(field.name)}
                  id={fieldID}
                  type={field.type}
                  autoComplete={field.autoComplete}
                  disabled={update.isPending}
                  aria-invalid={!!error}
                  aria-describedby={error ? `${fieldID}-error` : undefined}
                />
                {error && (
                  <FieldError id={`${fieldID}-error`}>{t(error)}</FieldError>
                )}
              </Field>
            )
          })}
          <div className='flex justify-end gap-2'>
            <Button
              type='button'
              variant='outline'
              disabled={update.isPending}
              onClick={props.onClose}
            >
              {t('Cancel')}
            </Button>
            <Button type='submit' disabled={!isDirty || update.isPending}>
              {update.isPending ? t('Saving...') : t('Save changes')}
            </Button>
          </div>
        </FieldGroup>
      </form>
    </Dialog>
  )
}
