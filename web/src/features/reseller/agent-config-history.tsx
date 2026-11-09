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
import { useInfiniteQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { useAuthStore } from '@/stores/auth-store'

import {
  resellerKey,
  resellerPath,
  resellerRequest,
  type ConfigChangePage,
  type ConfigSetting,
} from './api'
import { formatPersonalRatio } from './format'
import { ResellerQueryStatus, ResellerTimestamp } from './shared'

export function ResellerAgentConfigHistory(props: {
  userID: number
  revision: number
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const viewer = useAuthStore((s) => s.auth.user?.id)
  const query = useInfiniteQuery({
    meta: { errorToast: false },
    queryKey: resellerKey(
      viewer,
      'agent-config-history',
      props.userID,
      props.revision
    ),
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      resellerRequest<ConfigChangePage>(
        `${resellerPath(true)}/${props.userID}/config/history?before=${pageParam}`
      ),
    getNextPageParam: (page) => page.next_cursor || undefined,
  })

  function setting(value: ConfigSetting | null) {
    if (!value) return t('Not configured')
    return (
      <span className='flex flex-wrap items-center gap-x-2'>
        {value.authorized ? t('Authorized') : t('Not authorized')}
        <span className='text-muted-foreground tabular-nums'>
          {formatPersonalRatio(value.ratio, locale)}×
        </span>
      </span>
    )
  }

  return (
    <section aria-label={t('Configuration history')} className='mt-5 space-y-3'>
      <h3 className='font-medium'>{t('Configuration history')}</h3>
      <ResellerQueryStatus
        pending={query.isPending}
        error={query.isError && !query.isFetchNextPageError}
        retry={() => void query.refetch()}
      />
      {query.data?.pages[0]?.items.length === 0 && <p>{t('No results')}</p>}
      {query.data?.pages
        .flatMap((page) => page.items)
        .map((entry) => {
          const revision = t('Revision {{revision}}', {
            revision: formatNumber(entry.revision, locale),
          })
          return (
            <Collapsible key={entry.revision} className='rounded-lg border p-3'>
              <div className='flex flex-wrap items-center justify-between gap-2'>
                <div>
                  <p className='font-medium'>{revision}</p>
                  <p className='text-muted-foreground text-xs'>
                    {t('Operator')}: {formatNumber(entry.operator_id, locale)} ·{' '}
                    <ResellerTimestamp value={entry.created_at} />
                  </p>
                </div>
                {entry.changes.length > 0 ? (
                  <CollapsibleTrigger
                    aria-label={t('View changes for {{revision}}', {
                      revision,
                    })}
                    render={<Button variant='outline' size='sm' />}
                  >
                    {t('Details')}
                  </CollapsibleTrigger>
                ) : (
                  <p className='text-muted-foreground text-sm'>
                    {t('No changes')}
                  </p>
                )}
              </div>
              <CollapsibleContent className='pt-3'>
                <StaticDataTable
                  data={entry.changes}
                  getRowKey={(row) => `${row.model}:${row.channel_id}`}
                  columns={[
                    {
                      id: 'model',
                      header: t('Model'),
                      cell: (row) => row.model,
                    },
                    {
                      id: 'channel',
                      header: t('Channel'),
                      cell: (row) => formatNumber(row.channel_id, locale),
                    },
                    {
                      id: 'before',
                      header: t('Before'),
                      cell: (row) => setting(row.before),
                    },
                    {
                      id: 'after',
                      header: t('After'),
                      cell: (row) => setting(row.after),
                    },
                  ]}
                />
              </CollapsibleContent>
            </Collapsible>
          )
        })}
      {query.isFetchNextPageError && (
        <p role='alert'>{t('Operation failed')}</p>
      )}
      {query.hasNextPage && (
        <Button
          variant='outline'
          disabled={query.isFetching}
          onClick={() => void query.fetchNextPage()}
        >
          {t('Load more')}
        </Button>
      )}
    </section>
  )
}
