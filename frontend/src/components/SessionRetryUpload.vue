<template>
  <div class="rounded-xl border p-4 space-y-3" style="border-color: var(--brd)">
    <button type="button" class="btn-secondary w-full" @click="open = !open">
      {{ t('sessionRetry.action') }}
    </button>
    <form v-if="open" class="space-y-3" autocomplete="off" @submit.prevent="submit">
      <p class="text-sm text-muted">{{ t('sessionRetry.hint') }}</p>
      <div class="form-group">
        <label for="retry-session-cdk">{{ t('sessionRetry.cdkLabel') }}</label>
        <input id="retry-session-cdk" v-model="code" class="input mono" autocomplete="off" spellcheck="false" />
      </div>
      <div class="form-group">
        <label for="retry-session-value">{{ t('sessionRetry.sessionLabel') }}</label>
        <textarea
          id="retry-session-value"
          v-model="session"
          class="input h-36 font-mono text-xs"
          :placeholder="t('sessionRetry.placeholder')"
          autocomplete="off"
          spellcheck="false"
        />
      </div>
      <p v-if="message" class="text-sm" :class="ok ? 'alert alert-success' : 'alert alert-error'" role="status">{{ message }}</p>
      <button class="btn-primary w-full" type="submit" :disabled="busy">
        {{ busy ? t('sessionRetry.submitting') : t('sessionRetry.submit') }}
      </button>
    </form>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { activationSessionForRetry } from '../lib/batch-session'
import { cdkDevice } from '../lib/cdk-device'

const props = defineProps<{ initialCode?: string; startOpen?: boolean }>()
const { t } = useI18n({ useScope: 'global' })

const open = ref(!!props.startOpen)
const code = ref(props.initialCode || '')
const session = ref('')
const busy = ref(false)
const ok = ref(false)
const message = ref('')
const codeDirty = ref(false)

watch(() => props.startOpen, (value) => {
  if (value) open.value = true
})
watch(() => props.initialCode, (value) => {
  if (!codeDirty.value) code.value = value || ''
})
watch(code, (value) => {
  if (value !== (props.initialCode || '')) codeDirty.value = true
})

function feedback(status: number, error: string) {
  if (status === 200) return t('sessionRetry.success')
  if (error === 'invalid_session') return t('sessionRetry.invalid')
  if (status === 429 || error === 'rate_limited') return t('sessionRetry.rateLimit')
  if (status === 503 || error === 'unavailable') return t('sessionRetry.unavailable')
  return t('sessionRetry.failure')
}

async function submit() {
  if (busy.value) return
  message.value = ''
  ok.value = false
  const cdk = code.value.trim()
  const extracted = activationSessionForRetry(session.value)
  if (!cdk || !extracted) {
    message.value = !cdk ? t('sessionRetry.needCdk') : t('sessionRetry.invalid')
    return
  }
  busy.value = true
  try {
    const response = await fetch('/api/v1/public/cdk/retry-session', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Redemption-Device': cdkDevice(),
      },
      body: JSON.stringify({ code: cdk, session: extracted }),
    })
    const data = await response.json().catch(() => ({}))
    const error = typeof data?.error === 'string' ? data.error : ''
    ok.value = response.ok && data?.ok === true
    message.value = feedback(response.status, error)
    if (ok.value) session.value = ''
  } catch {
    message.value = t('sessionRetry.failure')
  } finally {
    busy.value = false
  }
}
</script>
