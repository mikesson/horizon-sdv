// Copyright (c) 2024-2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

/** Relative base so the same build can be mounted at any HTTP path (set at runtime via config.js `publicPath`). */
export default defineConfig({
  base: './',
  server: {
    proxy: {
      '/auth': {
        target: process.env.VITE_KEYCLOAK_ORIGIN || 'http://localhost:32080',
        changeOrigin: true,
      },
      '/api': {
        target: process.env.VITE_DEV_PROXY_TARGET || 'http://127.0.0.1:7090',
        changeOrigin: true,
      },
    },
  },
  plugins: [react()],
});
