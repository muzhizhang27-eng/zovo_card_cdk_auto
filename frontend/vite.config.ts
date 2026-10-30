import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  // Vue I18n's AST interpreter works under script-src 'self' without unsafe-eval.
  define: { __INTLIFY_JIT_COMPILATION__: true },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: process.env.VITE_API_TARGET || 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    // 生产不再对外发布 sourcemap：原来每次构建 8 MB 的 .map 跟着上线，等于公开完整源码。
    sourcemap: false,
    rollupOptions: {
      output: {
        // 框架与组件库单独成块：业务代码改动不会让用户重新下载这些不变的依赖。
        manualChunks(id) {
          if (!id.includes('node_modules')) return
          if (id.includes('element-plus') || id.includes('@element-plus')) return 'vendor-element'
          if (id.includes('chart.js') || id.includes('vue-chartjs')) return 'vendor-chart'
          if (id.includes('xlsx')) return 'vendor-xlsx'
          if (/[\\/]node_modules[\\/](vue|@vue|vue-router|pinia|vue-i18n|@intlify)[\\/]/.test(id)) return 'vendor-vue'
          return undefined
        },
      },
    },
  },
})
