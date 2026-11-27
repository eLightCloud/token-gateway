export type ActivationLink = {
  token: string
  user_id: number
  purpose: string | null
}

export function rememberAgentActivation(bindingID?: string) {
  const url = new URL(window.location.pathname, window.location.origin)
  url.searchParams.set('reseller', '1')
  if (bindingID) url.searchParams.set('binding', bindingID)
  window.history.replaceState(
    window.history.state,
    '',
    url.pathname + url.search
  )
}
