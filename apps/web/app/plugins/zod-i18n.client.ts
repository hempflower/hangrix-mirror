import * as z from 'zod'

export default defineNuxtPlugin((nuxtApp) => {
  const i18n = nuxtApp.$i18n as { t: (key: string, params?: Record<string, unknown>) => string }
  const t = (key: string, params?: Record<string, unknown>) => i18n.t(key, params)

  z.setErrorMap((issue, ctx) => {
    switch (issue.code) {
      case z.ZodIssueCode.invalid_type: {
        if (issue.received === 'undefined' || issue.received === 'null') {
          return { message: t('validation.required') }
        }
        return { message: t('validation.invalidType', { expected: issue.expected }) }
      }

      case z.ZodIssueCode.too_small: {
        if (issue.type === 'string') {
          if (issue.minimum === 1) return { message: t('validation.required') }
          return { message: t('validation.string.min', { min: issue.minimum }) }
        }
        if (issue.type === 'number') {
          return { message: t('validation.number.min', { min: issue.minimum }) }
        }
        if (issue.type === 'array') {
          return { message: t('validation.array.min', { min: issue.minimum }) }
        }
        return { message: ctx.defaultError }
      }

      case z.ZodIssueCode.too_big: {
        if (issue.type === 'string') {
          return { message: t('validation.string.max', { max: issue.maximum }) }
        }
        if (issue.type === 'number') {
          return { message: t('validation.number.max', { max: issue.maximum }) }
        }
        if (issue.type === 'array') {
          return { message: t('validation.array.max', { max: issue.maximum }) }
        }
        return { message: ctx.defaultError }
      }

      case z.ZodIssueCode.invalid_string: {
        if (issue.validation === 'email') return { message: t('validation.string.email') }
        if (issue.validation === 'url') return { message: t('validation.string.url') }
        if (issue.validation === 'uuid') return { message: t('validation.string.uuid') }
        if (issue.validation === 'regex') return { message: t('validation.string.regex') }
        return { message: ctx.defaultError }
      }

      case z.ZodIssueCode.invalid_enum_value:
        return { message: t('validation.invalidEnum') }

      default:
        return { message: ctx.defaultError }
    }
  })
})
