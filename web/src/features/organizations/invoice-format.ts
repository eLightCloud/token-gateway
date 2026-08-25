const USD_FORMATTER = new Intl.NumberFormat('en-US', {
  minimumFractionDigits: 4,
  maximumFractionDigits: 4,
})

export function formatOrganizationInvoiceUSD(
  value: string,
  emptyForZero = false
): string {
  const amount = Number(value)
  if (!Number.isFinite(amount)) return '-'
  if (emptyForZero && amount === 0) return '-'
  return USD_FORMATTER.format(amount)
}
