/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
/**
 * Standalone entry for the API documentation page — renders the
 * backend-independent docs UI with no router/network, so it can be built into a
 * fully static bundle and mounted at any path behind a reverse proxy.
 */
import { StrictMode } from 'react'
import ReactDOM from 'react-dom/client'

import { StandaloneApp } from './standalone-app'
import './docs-i18n'

import '@/styles/index.css'

const rootElement = document.querySelector('#root')
if (rootElement) {
  ReactDOM.createRoot(rootElement).render(
    <StrictMode>
      <StandaloneApp />
    </StrictMode>
  )
}
