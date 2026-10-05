export function getModelMockUrl(): string | null {
  const configured = import.meta.env.VITE_MODEL_MOCK_URL?.trim()
  if (configured) {
    return new URL(configured, window.location.origin).href
  }
  if (import.meta.env.DEV) {
    return `${window.location.protocol}//${window.location.hostname}:4173/`
  }
  return null
}
