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
import type { TFunction } from 'i18next'

// Maps a server category_key to its localized display name. Standard keys are
// translated so the Alibaba Cloud supply indicator (and the Vector label) can
// localize per language; fallback (off-catalog, per-model) categories always
// show the raw server-provided name.
export function organizationInvoiceCategoryName(
  categoryKey: string,
  categoryName: string,
  fallback: boolean,
  t: TFunction
): string {
  if (fallback) return categoryName
  switch (categoryKey) {
    case 'claude':
      return t('Claude')
    case 'gpt':
      return t('GPT')
    case 'gemini':
      return t('Gemini')
    case 'deepseek':
      return t('Deepseek')
    case 'minimax':
      return t('Minimax (Alibaba Cloud)')
    case 'kimi':
      return t('Kimi (Alibaba Cloud)')
    case 'glm':
      return t('GLM (Alibaba Cloud)')
    case 'qwen':
      return t('Qwen (Alibaba Cloud)')
    case 'vector':
      return t('Vector')
    default:
      return categoryName
  }
}
