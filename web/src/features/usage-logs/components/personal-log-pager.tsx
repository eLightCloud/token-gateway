import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

const sizes = [10, 20, 30, 40, 50, 100].map((size) => ({
  value: String(size),
  label: String(size),
}))

export function PersonalLogPager(props: {
  canGoBack: boolean
  canGoNext: boolean
  busy: boolean
  pageSize: number
  onPageSizeChange: (size: number) => void
  onRefresh: () => void
  onPrevious: () => void
  onNext: () => void
}) {
  const { t } = useTranslation()
  return (
    <nav
      aria-label={t('Page')}
      className='flex flex-wrap items-center justify-end gap-2'
    >
      <Button
        variant='outline'
        size='sm'
        className='max-sm:h-11'
        disabled={props.busy}
        onClick={props.onRefresh}
      >
        {t('Refresh latest')}
      </Button>
      <Select
        items={sizes}
        value={String(props.pageSize)}
        disabled={props.busy}
        onValueChange={(value) => {
          if (value) props.onPageSizeChange(Number(value))
        }}
      >
        <SelectTrigger aria-label={t('Rows per page')} className='max-sm:h-11'>
          <SelectValue />
        </SelectTrigger>
        <SelectContent side='top'>
          <SelectGroup>
            {sizes.map((size) => (
              <SelectItem key={size.value} value={size.value}>
                {size.label}
              </SelectItem>
            ))}
          </SelectGroup>
        </SelectContent>
      </Select>
      <Button
        variant='outline'
        size='sm'
        className='max-sm:h-11'
        disabled={!props.canGoBack || props.busy}
        onClick={props.onPrevious}
      >
        {t('Previous')}
      </Button>
      <Button
        variant='outline'
        size='sm'
        className='max-sm:h-11'
        disabled={!props.canGoNext || props.busy}
        onClick={props.onNext}
      >
        {t('Next')}
      </Button>
    </nav>
  )
}
