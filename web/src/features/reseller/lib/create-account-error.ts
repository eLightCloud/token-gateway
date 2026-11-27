import type { UseFormReturn } from 'react-hook-form'

import {
  handleServerError,
  markServerErrorHandled,
} from '@/lib/handle-server-error'

import { getResellerAccountErrorDetails } from './account-error-details'
import type { ResellerCreateValues } from './create-account-schema'

const createFields = new Set<unknown>([
  'username',
  'display_name',
  'email',
  'phone',
  'remark',
])

export function showResellerCreateError(
  error: unknown,
  values: ResellerCreateValues,
  form: UseFormReturn<ResellerCreateValues>,
  fallback: string
): void {
  const details = getResellerAccountErrorDetails(error)
  if (!details || !createFields.has(details.field)) {
    handleServerError(error, fallback)
    return
  }

  const field = details.field as keyof ResellerCreateValues
  markServerErrorHandled(error)
  let current = form.getValues(field)
  if (field !== 'display_name') current = current.trim()
  const submitted = values[field]
  const unchanged =
    field === 'email'
      ? current.toLowerCase() === submitted.toLowerCase()
      : current === submitted
  if (!unchanged) return

  form.setError(
    field,
    { type: 'server', message: details.messageKey },
    { shouldFocus: true }
  )
}
