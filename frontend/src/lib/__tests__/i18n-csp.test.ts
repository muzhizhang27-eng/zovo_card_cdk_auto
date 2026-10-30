import { expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import zh from '../../i18n/locales/zh'
import en from '../../i18n/locales/en'

it('renders production messages when string-to-code evaluation is disabled by CSP', () => {
  const original = globalThis.Function
  let zhTitle = '', enTitle = '', interpolated = ''
  try {
    globalThis.Function = new Proxy(original, { construct() { throw new Error('unsafe-eval blocked') }, apply() { throw new Error('unsafe-eval blocked') } })
    const i18n = createI18n({ legacy: false, locale: 'zh', messages: { zh, en } })
    zhTitle = i18n.global.t('login.title')
    i18n.global.locale.value = 'en'
    enTitle = i18n.global.t('login.title')
    interpolated = i18n.global.t('batch.excelHint')
  } finally { globalThis.Function = original }
  expect(zhTitle).toBe(zh.login.title)
  expect(enTitle).toBe(en.login.title)
  expect(interpolated).not.toContain('batch.excelHint')
})
