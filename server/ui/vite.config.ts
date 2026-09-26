import fs from 'node:fs'
import path from 'node:path'
import zlib from 'node:zlib'
import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// 产物在 dist/，由 Go 用 go:embed 编进 rcweb 二进制（见 ../static.go）。
// 开发时 npm run dev，接口代理到本机跑着的 rcweb（rcweb dev 默认 7681）。
const backend = process.env.RCWEB_DEV_BACKEND ?? '127.0.0.1:7681'

// 把 js / css 预先压成 .br 放在旁边，rcweb 见浏览器支持就直接发。
// Go 标准库只有 gzip，brotli 最高档又慢，放在构建时压一次正好
function brotli(): Plugin {
  return {
    name: 'rcweb-brotli',
    apply: 'build',
    writeBundle(opts, bundle) {
      for (const name of Object.keys(bundle)) {
        if (!/\.(js|css|html|svg)$/.test(name)) continue
        const file = path.join(opts.dir!, name)
        const data = fs.readFileSync(file)
        if (data.length < 1024) continue
        fs.writeFileSync(file + '.br', zlib.brotliCompressSync(data, { params: { [zlib.constants.BROTLI_PARAM_QUALITY]: 11 } }))
      }
    },
  }
}

export default defineConfig({
  plugins: [react(), tailwindcss(), brotli()],
  resolve: { alias: { '@': path.resolve(import.meta.dirname, 'src') } },
  build: { outDir: 'dist', emptyOutDir: true, chunkSizeWarningLimit: 2000 },
  server: {
    // Host 原样转给后端：后端拿 Origin 和 Host 比对防 CSRF，Host 被改成后端地址的话写接口全会被拒
    proxy: {
      '/api': { target: `http://${backend}`, changeOrigin: false, xfwd: true },
      '/ws': { target: `ws://${backend}`, ws: true, changeOrigin: false, xfwd: true },
    },
  },
})
