import tailwindcss from '@tailwindcss/vite'

export default defineNuxtConfig({
  compatibilityDate: '2026-05-13',
  devtools: { enabled: true },
  modules: ['shadcn-nuxt', '@nuxtjs/i18n'],
  i18n: {
    strategy: 'no_prefix',
    defaultLocale: 'zh-CN',
    locales: [
      { code: 'zh-CN', name: '简体中文', file: 'zh-CN.json' },
      { code: 'en', name: 'English', file: 'en.json' },
    ],
    detectBrowserLanguage: {
      useCookie: true,
      cookieKey: 'hangrix_locale',
      redirectOn: 'root',
      alwaysRedirect: false,
      fallbackLocale: 'zh-CN',
    },
  },
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
      '/api': { target: 'http://localhost:8080/api', changeOrigin: true },
      // Forward git smart-HTTP so the clone URL displayed in the UI
      // (which uses window.location.origin = the dev host) actually works.
      // In prod the single binary serves /git itself, no proxy needed.
      '/git': { target: 'http://localhost:8080/git', changeOrigin: true },
      // Runner install endpoints (bash script + binary). Same story as
      // /git — in prod the single binary serves these itself; in dev
      // Nuxt is the public entrypoint, so without this proxy a fresh
      // `curl localhost:3000/install/runner.sh` would hit the SPA
      // fallback and download index.html instead of the script.
      '/install': { target: 'http://localhost:8080/install', changeOrigin: true },
    }
  }
})
