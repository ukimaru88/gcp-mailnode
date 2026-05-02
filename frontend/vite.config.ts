import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import fs from 'fs'
import path from 'path'

const projectRoot = fs.realpathSync(__dirname)

export default defineConfig({
  root: projectRoot,
  plugins: [react()],
  base: './',
  resolve: {
    alias: {
      "@": path.resolve(projectRoot, "./src"),
    },
  },
})
