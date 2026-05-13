const PUBLIC_ROUTES = new Set(['/login', '/register'])

export default defineNuxtRouteMiddleware((to) => {
  const { user } = useCurrentUser()
  const isPublic = PUBLIC_ROUTES.has(to.path)

  if (!user.value && !isPublic) {
    return navigateTo({ path: '/login', query: { next: to.fullPath } })
  }
  if (user.value && isPublic) {
    return navigateTo('/')
  }
  if (to.path.startsWith('/admin') && user.value?.role !== 'admin') {
    return navigateTo('/')
  }
})
