import { getServerErrorSources } from '@/lib/server-error-message'

type ResellerAccountErrorDetails = {
  field: unknown
  emailUpdated: boolean
  messageKey: string
}

export function getResellerAccountErrorDetails(
  error: unknown
): ResellerAccountErrorDetails | undefined {
  for (const source of getServerErrorSources(error)) {
    const { data, message_key: messageKey } = source
    if (!data || typeof data !== 'object' || typeof messageKey !== 'string') {
      continue
    }
    return {
      field: 'field' in data ? data.field : undefined,
      emailUpdated: 'email_updated' in data && data.email_updated === true,
      messageKey,
    }
  }
}
