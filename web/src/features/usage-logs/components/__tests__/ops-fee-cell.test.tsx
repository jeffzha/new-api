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
import { render, screen } from '@testing-library/react'
import { createInstance } from 'i18next'
import { describe, expect, test } from 'vitest'

import zh from '@/i18n/locales/zh.json'

import { usageLogSchema } from '../../data/schema'
import { OpsFeeCell } from '../ops-fee-cell'

describe('operations fee display', () => {
  test('Chinese users see translated fee labels and calculation states', async () => {
    const i18n = createInstance()
    await i18n.init({ lng: 'zh', resources: { zh } })
    expect(i18n.t('Operations calculated fee (CNY)')).toBe(
      '运营计算费用（人民币）'
    )
    expect(i18n.t('Operations fee (CNY)')).toBe('运营计算费用（人民币）')
    expect(i18n.t('Awaiting operations fee')).toBe('等待运营计算')
    expect(i18n.t('Operations fee unconfirmed')).toBe('运营费用待确认')
    expect(i18n.t('Operations fee received')).toBe('已取得运营费用')
    expect(i18n.t('No operations fee for this call')).toBe(
      '该调用未纳入运营扣费'
    )
  })

  test('waiting and unconfirmed fees never appear as free', () => {
    const view = render(
      <OpsFeeCell fee={{ status: 'waiting', amount: null, currency: 'CNY' }} />
    )
    expect(screen.getByText('Awaiting operations fee')).toBeInTheDocument()
    expect(screen.queryByText(/¥/)).not.toBeInTheDocument()
    view.rerender(
      <OpsFeeCell
        fee={{ status: 'unconfirmed', amount: null, currency: 'CNY' }}
      />
    )
    expect(screen.getByText('Operations fee unconfirmed')).toBeInTheDocument()
    expect(screen.queryByText(/¥/)).not.toBeInTheDocument()
  })

  test('zero is a real amount and decimal precision is retained', () => {
    const view = render(
      <OpsFeeCell fee={{ status: 'available', amount: '0', currency: 'CNY' }} />
    )
    expect(screen.getByLabelText('¥0')).toBeInTheDocument()
    view.rerender(
      <OpsFeeCell
        fee={{ status: 'available', amount: '0.000001', currency: 'CNY' }}
      />
    )
    expect(screen.getByLabelText('¥0.000001')).toBeInTheDocument()
    expect(
      screen.queryByText('Operations fee received')
    ).not.toBeInTheDocument()
  })

  test('original log remains compatible when fee readback is disabled', () => {
    const log = usageLogSchema.parse({
      id: 1,
      user_id: 1,
      created_at: 1,
      type: 2,
      content: '',
    })
    expect(log.ops_fee).toBeUndefined()
    expect(log.quota).toBe(0)
    render(<OpsFeeCell fee={log.ops_fee} />)
    expect(screen.getByText('—')).toBeInTheDocument()
  })
})
