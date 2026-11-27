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
import {
  BillingExpressionError,
  visitExpression,
  type ExpressionNode,
} from './types'

export function convertCNY(
  amount: number,
  rate: number,
  position: number
): number {
  if (!Number.isFinite(rate) || rate <= 0) {
    throw new BillingExpressionError({
      code: 'missing_context',
      detail: 'CNY exchange rate',
      position,
    })
  }
  const usd = amount / rate
  if (!Number.isFinite(usd) || usd < 0) {
    throw new BillingExpressionError({
      code: 'number',
      detail: 'CNY price',
      position,
    })
  }
  return usd
}

export function scaleFixedUSD(amount: number, position: number): number {
  const scaled = amount * 1_000_000
  if (!Number.isFinite(scaled) || amount < 0) {
    throw new BillingExpressionError({
      code: 'number',
      detail: 'fixed price',
      position,
    })
  }
  return scaled
}

export function validateCNYCall(node: ExpressionNode, version: 1 | 2): void {
  if (node.kind !== 'call' || node.name !== 'cny_to_usd') return
  const amount = node.args[0]
  if (version !== 2) {
    throw new BillingExpressionError({
      code: 'unsupported',
      detail: 'cny_to_usd requires v2',
      position: node.start,
    })
  }
  if (
    amount.kind !== 'literal' ||
    typeof amount.value !== 'number' ||
    !Number.isFinite(amount.value) ||
    amount.value < 0
  ) {
    throw new BillingExpressionError({
      code: 'number',
      detail: 'cny_to_usd',
      position: node.start,
    })
  }
}

export function validateCNYConversion(ast: ExpressionNode, rate: number): void {
  visitExpression(ast, (node) => {
    if (node.kind !== 'call') return
    if (node.name === 'cny_to_usd' && node.args[0].kind === 'literal') {
      convertCNY(Number(node.args[0].value), rate, node.start)
    } else if (node.name === 'fixed') {
      const arg = node.args[0]
      if (
        arg.kind === 'call' &&
        arg.name === 'cny_to_usd' &&
        arg.args[0].kind === 'literal'
      ) {
        scaleFixedUSD(
          convertCNY(Number(arg.args[0].value), rate, arg.start),
          node.start
        )
      }
    }
  })
}
