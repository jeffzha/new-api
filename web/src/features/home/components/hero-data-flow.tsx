/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
export function HeroDataFlow() {
  return (
    <div className='gateway-data-flow' aria-hidden='true'>
      {['left', 'right'].map((side) => (
        <div
          key={side}
          className={`gateway-data-flow-side gateway-data-flow-${side}`}
        >
          <span className='gateway-data-rail gateway-data-rail-primary' />
          <span className='gateway-data-rail gateway-data-rail-secondary' />
          <span className='gateway-data-rail gateway-data-rail-tertiary' />
        </div>
      ))}
    </div>
  )
}
