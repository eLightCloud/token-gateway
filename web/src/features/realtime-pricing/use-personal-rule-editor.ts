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
import { useCallback, useMemo, useState } from 'react'
import { type Control, useForm, useWatch } from 'react-hook-form'

import type { PricingRule } from './api'
import { ruleSchema } from './lib/rule-schema'

export type PersonalRuleEdit = {
  model: string
  control: Control<PricingRule>
  isNew: boolean
  cancel: () => void
}

export function usePersonalRuleEditor(items: PricingRule[]) {
  const [model, setModel] = useState<string | null>(null)
  const form = useForm<PricingRule>({
    defaultValues: { model: '', ratio: '1', channel_ids: [] },
    resolver: zodResolver(ruleSchema),
    mode: 'onChange',
  })
  const { control, reset } = form
  const [ratio, channelIDs] = useWatch({
    control,
    name: ['ratio', 'channel_ids'],
  })
  const existing = items.find((rule) => rule.model === model)
  const isNew = !existing
  const begin = useCallback(
    (rule: PricingRule) => {
      reset(rule)
      setModel(rule.model)
    },
    [reset]
  )
  const cancel = useCallback(() => setModel(null), [])
  const edit = useMemo<PersonalRuleEdit | undefined>(
    () => (model ? { model, control, isNew, cancel } : undefined),
    [model, control, isNew, cancel]
  )

  const rule = useMemo(
    () =>
      model ? { ...existing, model, ratio, channel_ids: channelIDs } : null,
    [existing, model, ratio, channelIDs]
  )
  const draft = useMemo(() => {
    if (!rule) return items
    return existing
      ? items.map((item) => (item.model === rule.model ? rule : item))
      : [rule, ...items]
  }, [items, existing, rule])

  return {
    begin,
    cancel,
    edit,
    items: draft,
    isValid: !rule || ruleSchema.safeParse(rule).success,
  }
}
