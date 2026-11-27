import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { SystemBehaviorSection } from '../system-behavior-section'

function Fixture() {
  const [actions, setActions] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setActions} />
      <SettingsPageProvider actionsContainer={actions}>
        <SystemBehaviorSection
          defaultValues={{
            DefaultCollapseSidebar: false,
            DemoSiteEnabled: false,
            SelfUseModeEnabled: false,
            domesticModelsOnly: true,
            discountedModelsOnly: true,
          }}
        />
      </SettingsPageProvider>
    </>
  )
}

test.each([
  [
    'Show domestic models only',
    'Show discounted models only',
    'model_visibility.domestic_only',
  ],
  [
    'Show discounted models only',
    'Show domestic models only',
    'model_visibility.discounted_only',
  ],
])('turning off %s saves only that switch', async (label, otherLabel, key) => {
  const save = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const client = new QueryClient()
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={client}>
      <Fixture />
    </QueryClientProvider>
  )

  const toggle = screen.getByRole('switch', { name: label })
  const otherToggle = screen.getByRole('switch', { name: otherLabel })
  expect(toggle).toBeChecked()
  expect(otherToggle).toBeChecked()
  await user.click(toggle)
  expect(toggle).not.toBeChecked()
  expect(otherToggle).toBeChecked()
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))

  await waitFor(() => {
    expect(save).toHaveBeenCalledExactlyOnceWith('/api/option/', {
      key,
      value: false,
    })
  })
})

test('the visitor setting points to automatic classification and vendor management', () => {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <Fixture />
    </QueryClientProvider>
  )

  expect(
    screen.getByRole('switch', { name: 'Show domestic models only' })
  ).toHaveAccessibleDescription(/Common vendors are classified automatically/)
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})
