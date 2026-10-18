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
  beijingDateBoundaryToUnix,
  formatTimestampInBeijingTime,
} from './beijing-time'
import type {
  OrganizationInvoice,
  OrganizationInvoicePeriod,
} from './invoice-types'

export type OrganizationInvoiceReviewIssue = {
  user_id: number
  task_id?: string
  reason: string
}

// Presentation only: use persisted evidence, never infer new charges or gaps
// from differences between historical and query-time balances.
export function organizationInvoiceNeedsExpenseReview(
  invoice?: OrganizationInvoice
): boolean {
  if (!invoice || invoice.generation_status === 'generating') return false
  const coverage = invoice.expense_coverage
  return (
    !coverage ||
    coverage.status !== 'complete' ||
    coverage.pending_tasks > 0 ||
    coverage.unknown_rows > 0 ||
    (coverage.pending_source_rows ?? 0) > 0 ||
    Boolean(coverage.issues?.length)
  )
}

export function getOrganizationInvoiceReview(invoice: OrganizationInvoice): {
  expenseNeedsReview: boolean
  expenseIssues: OrganizationInvoiceReviewIssue[]
  walletIssues: OrganizationInvoiceReviewIssue[]
} {
  const expenseNeedsReview = organizationInvoiceNeedsExpenseReview(invoice)
  const coverage = invoice.expense_coverage
  const expenseIssues = new Map<string, OrganizationInvoiceReviewIssue>()
  const walletIssues = new Map<string, OrganizationInvoiceReviewIssue>()
  for (const issue of coverage?.issues ?? []) {
    expenseIssues.set(
      JSON.stringify([issue.user_id, issue.task_id ?? '', issue.reason]),
      issue
    )
  }
  if (expenseNeedsReview && expenseIssues.size === 0) {
    const reason = coverage?.status
      ? 'expense_evidence_incomplete'
      : 'expense_coverage_missing'
    expenseIssues.set(reason, { user_id: 0, reason })
  }
  const walletUsers = new Set<number>()
  for (const issue of coverage?.wallet_issues ?? []) {
    walletIssues.set(
      JSON.stringify([issue.user_id, issue.task_id ?? '', issue.reason]),
      issue
    )
    walletUsers.add(issue.user_id)
  }
  for (const account of invoice.accounts) {
    const status = account.financials.reconciliation_status
    if (
      walletUsers.has(account.user_id) ||
      (status !== 'incomplete' && status !== 'unavailable')
    ) {
      continue
    }
    const reason =
      status === 'unavailable'
        ? 'wallet_information_unavailable'
        : 'wallet_evidence_incomplete'
    walletIssues.set(JSON.stringify([account.user_id, '', reason]), {
      user_id: account.user_id,
      reason,
    })
  }
  // The warning may refer to an organization-wide or query-time read failure.
  // Its raw text can contain SQL or connection details; never render it.
  if (invoice.wallet_warning) {
    walletIssues.set(
      JSON.stringify([0, '', 'wallet_information_unavailable']),
      { user_id: 0, reason: 'wallet_information_unavailable' }
    )
  }
  return {
    expenseNeedsReview,
    expenseIssues: [...expenseIssues.values()],
    walletIssues: [...walletIssues.values()],
  }
}

export function formatOrganizationInvoiceCoverage(
  period?: OrganizationInvoicePeriod
): { start: string; end: string } | undefined {
  if (!period?.start_date || !period.end_date) return undefined
  let start = period.start_date
  let end = period.end_date
  if (
    period.start_timestamp > 0 &&
    period.start_timestamp !== beijingDateBoundaryToUnix(start, false)
  ) {
    start = formatTimestampInBeijingTime(period.start_timestamp)
  }
  if (
    period.end_timestamp > 0 &&
    period.end_timestamp !== beijingDateBoundaryToUnix(end, true)
  ) {
    end = formatTimestampInBeijingTime(period.end_timestamp)
  }
  return { start, end }
}
