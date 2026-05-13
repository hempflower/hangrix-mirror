import tailwindcss from '@tailwindcss/vite'

export default defineNuxtConfig({
  compatibilityDate: '2026-05-13',
  devtools: { enabled: true },
  modules: ['shadcn-nuxt'],
  css: ['~/assets/css/tailwind.css'],
  ssr: false,
  // nuxt issue: https://github.com/nuxt/nuxt/issues/35033
  experimental: {
    viteEnvironmentApi: true,
  },
  app: {
    head: {
      htmlAttrs: { class: 'dark' },
    },
  },
  vite: {
    plugins: [tailwindcss()]
  },
  shadcn: {
    prefix: '',
    componentDir: '@/components/ui'
  },
  typescript: {
    strict: true,
    typeCheck: false
  },
  runtimeConfig: {
    public: {
      apiBase: ''
    }
  },
  nitro: {
    devProxy: {
      '/api': {target: 'http://localhost:8080/api', changeOrigin: true}
    }
  }
})
