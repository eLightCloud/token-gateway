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
import { useTranslation } from 'react-i18next'

import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { SettingsControlChildren } from '@/features/system-settings/components/settings-form-layout'
import { getCurrencyDisplay, getCurrencyLabel } from '@/lib/currency'
import { formatQuota } from '@/lib/format'

import type { WalletReminderMode } from '../../types'

type WalletReminderFieldsProps = {
  mode: WalletReminderMode
  threshold: string
  subscriptionThreshold: number
  onModeChange: (mode: WalletReminderMode) => void
  onThresholdChange: (value: string) => void
}

export function WalletReminderFields({
  mode,
  threshold,
  subscriptionThreshold,
  onModeChange,
  onThresholdChange,
}: WalletReminderFieldsProps) {
  const { t } = useTranslation()
  return (
    <SettingsControlChildren className='space-y-2 pb-1'>
      <Label id='wallet-reminder-mode'>{t('Wallet reminder schedule')}</Label>
      <ToggleGroup
        aria-labelledby='wallet-reminder-mode'
        value={[mode]}
        variant='outline'
        spacing={2}
        className='flex-wrap'
        onValueChange={([value]) => {
          if (value === 'tiers' || value === 'step' || value === 'custom') {
            onModeChange(value)
          }
        }}
      >
        <ToggleGroupItem value='tiers'>{t('Default tiers')}</ToggleGroupItem>
        <ToggleGroupItem value='step'>{t('Every $10')}</ToggleGroupItem>
        <ToggleGroupItem value='custom'>
          {t('Custom threshold')}
        </ToggleGroupItem>
      </ToggleGroup>
      {mode === 'tiers' && (
        <p className='text-muted-foreground text-xs'>
          {t('Wallet reminders at $100, $30, $10, and $2 USD')}
        </p>
      )}
      {mode === 'step' && (
        <p className='text-muted-foreground text-xs'>
          {t('Wallet reminders every $10 from $100 to $10 USD, plus $2 USD')}
        </p>
      )}
      {mode === 'custom' && (
        <>
          <Label htmlFor='threshold'>
            {t('Reminder threshold')} ({getCurrencyLabel()})
          </Label>
          <Input
            id='threshold'
            type='number'
            className='h-9'
            min={0}
            step={getCurrencyDisplay().meta.kind === 'tokens' ? 1 : 0.000001}
            value={threshold}
            onChange={(event) => onThresholdChange(event.target.value)}
          />
          <p className='text-muted-foreground text-xs'>
            {t(
              'Leave empty to use the default wallet tiers and the site subscription threshold ({{amount}})',
              {
                amount: formatQuota(subscriptionThreshold),
              }
            )}
          </p>
        </>
      )}
    </SettingsControlChildren>
  )
}
