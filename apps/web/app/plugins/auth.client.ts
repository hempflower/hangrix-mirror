// Hydrate the current user on app boot so `useCurrentUser` is populated
// before middleware runs, avoiding a flash of the login screen when a
// session cookie is already present.
export default defineNuxtPlugin(async () => {
  const { refresh } = useCurrentUser()
  await refresh()
})
