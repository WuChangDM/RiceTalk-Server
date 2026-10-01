import '@testing-library/jest-dom'

// jsdom under Node 26 may not expose localStorage; provide a minimal in-memory polyfill
// so that stores and api-core tests can run without crashing.
if (typeof globalThis.localStorage === 'undefined') {
  const store: Record<string, string> = {}
  const storage: Storage = {
    getItem(key) {
      return store[key] ?? null
    },
    setItem(key, value) {
      store[key] = String(value)
    },
    removeItem(key) {
      delete store[key]
    },
    clear() {
      Object.keys(store).forEach((k) => delete store[k])
    },
    get length() {
      return Object.keys(store).length
    },
    key(index) {
      const keys = Object.keys(store)
      return keys[index] ?? null
    },
  }
  Object.defineProperty(globalThis, 'localStorage', {
    value: storage,
    configurable: true,
    writable: true,
  })
}
