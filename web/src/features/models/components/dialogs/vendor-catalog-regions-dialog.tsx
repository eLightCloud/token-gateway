import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { MODEL_CATALOG_QUERY_KEY } from '@/features/pricing/hooks/use-model-catalog'
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

type Region = 'domestic' | 'overseas' | 'unclassified'
type Selection = Region | 'auto'
type VendorRegion = {
  name: string
  region: Region
  automatic_region: Region
  source: 'built_in' | 'legacy' | 'override' | 'unknown'
  models: string[]
}

const endpoint = '/api/vendors/catalog-regions'
const queryKey = ['vendors', 'catalog-regions'] as const

export function VendorCatalogRegionsDialog() {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [draft, setDraft] = useState<Selection | null>(null)
  const vendors = useQuery({
    queryKey,
    enabled: open,
    queryFn: async ({ signal }) => {
      const { data } = await api.get<{
        success: boolean
        data: VendorRegion[]
      }>(endpoint, { signal, disableDuplicate: true })
      return requireServerSuccess(data).data
    },
  })
  const save = useMutation({
    mutationFn: async (region: Selection) => {
      const { data } = await api.put(endpoint, { name, region })
      requireServerSuccess(data)
    },
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey })
      void client.invalidateQueries({ queryKey: MODEL_CATALOG_QUERY_KEY })
      toast.success(t('Vendor region saved'))
      setDraft(null)
    },
  })

  const selected = vendors.data?.find((vendor) => vendor.name === name)
  const current: Selection =
    selected?.source === 'override' ? selected.region : 'auto'
  const choice = draft ?? current
  const proposed = choice === 'auto' ? selected?.automatic_region : choice
  const canSave = selected && choice !== current && !vendors.isError
  const visibleNow = selected?.region === 'domestic'
  const visibleAfter = proposed === 'domestic'
  let preview = t('No change to the visitor catalog.')
  if (selected && visibleAfter !== visibleNow && selected.models.length > 0) {
    preview = visibleAfter
      ? t('These models will appear in the visitor catalog.')
      : t('These models will be removed from the visitor catalog.')
  }
  const labels = {
    domestic: t('Domestic'),
    overseas: t('Overseas'),
    unclassified: t('Unclassified'),
  }
  const sources = {
    built_in: t('Built-in classification'),
    legacy: t('Previous domestic vendor list'),
    override: t('Administrator override'),
    unknown: t('No automatic match'),
  }

  function changeOpen(next: boolean) {
    if (save.isPending) return
    setOpen(next)
    if (!next) {
      setDraft(null)
      setName('')
      save.reset()
    }
  }

  return (
    <Dialog
      trigger={
        <Button size='sm' variant='outline'>
          {t('Vendor regions')}
        </Button>
      }
      open={open}
      onOpenChange={changeOpen}
      title={t('Vendor regions')}
      description={t(
        'Preview what signed-out visitors see when the domestic-only filter is enabled.'
      )}
      footer={
        <>
          <Button
            variant='outline'
            disabled={save.isPending}
            onClick={() => changeOpen(false)}
          >
            {t('Close')}
          </Button>
          <Button
            disabled={!canSave || save.isPending}
            onClick={() => save.mutate(choice)}
          >
            {t('Save changes')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        {vendors.isLoading && <LoadingState />}
        {vendors.isError && (
          <ErrorState
            title={t('Failed to load vendors')}
            onRetry={() => void vendors.refetch()}
          />
        )}
        {vendors.data && !vendors.isError && (
          <div className='space-y-2'>
            <Label htmlFor='catalog-vendor'>{t('Vendor')}</Label>
            <Combobox
              id='catalog-vendor'
              options={vendors.data.map((vendor) => ({
                label: vendor.name,
                value: vendor.name,
              }))}
              value={name}
              onValueChange={(value) => {
                setName(value ?? '')
                setDraft(null)
                save.reset()
              }}
              disabled={save.isPending}
              placeholder={t('Select a vendor')}
              searchPlaceholder={t('Search vendors')}
              emptyText={t('No vendors found')}
            />
          </div>
        )}
        {selected && !vendors.isError && (
          <>
            <div className='space-y-2'>
              <Label htmlFor='catalog-vendor-region'>
                {t('Classification')}
              </Label>
              <NativeSelect
                id='catalog-vendor-region'
                value={choice}
                disabled={save.isPending}
                onChange={(event) => {
                  setDraft(event.currentTarget.value as Selection)
                  save.reset()
                }}
              >
                <NativeSelectOption value='auto'>
                  {t('Automatic ({{region}})', {
                    region: labels[selected.automatic_region],
                  })}
                </NativeSelectOption>
                {Object.entries(labels).map(([region, label]) => (
                  <NativeSelectOption key={region} value={region}>
                    {label}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
              <p className='text-muted-foreground text-sm'>
                {t('Current classification: {{region}} ({{source}})', {
                  region: labels[selected.region],
                  source: sources[selected.source],
                })}
              </p>
            </div>
            <div className='space-y-2 text-sm' aria-live='polite'>
              <p>{preview}</p>
              {selected.models.length > 0 && (
                <ul className='max-h-40 list-inside list-disc overflow-y-auto break-all'>
                  {selected.models.map((model) => (
                    <li key={model}>{model}</li>
                  ))}
                </ul>
              )}
            </div>
          </>
        )}
      </div>
    </Dialog>
  )
}
