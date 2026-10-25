// Pocket Money's purchases that have a place (a scanned receipt's shop),
// and which visit each happened during.
import { useEffect, useState } from 'react'
import { api, V1 } from './api'
import { haversine } from './geo'
import type { Visit } from './types'

export interface Purchase {
  id: number; date: string; time: string; ts: number; time_known: boolean; lat: number; lng: number; kind: string
  payee: string; description: string; amount: number; currency: string; category: string; category_icon: string
  shop?: string; address?: string; city?: string; items?: number; url: string
}

export interface PurchasesView { connected: boolean; via?: string; detail?: string; open_url?: string; items: Purchase[] }

/** The located purchases in a date range (empty when Pocket Money is not connected). */
export function usePurchases(from: string, to: string, tick = 0): PurchasesView | null {
  const [v, setV] = useState<PurchasesView | null>(null)
  useEffect(() => {
    let live = true
    api.get<PurchasesView>(`${V1}/purchases?from=${from}&to=${to}`).then(r => { if (live) setV(r) }).catch(() => { if (live) setV(null) })
    return () => { live = false }
  }, [from, to, tick])
  return v
}

export const shopOf = (p: Purchase) => p.shop || p.payee || p.description || 'a shop'

export function fmtMoney(minor: number, currency: string): string {
  const v = (minor / 100).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })
  return currency === 'BAM' ? `${v} KM` : currency === 'EUR' ? `€${v}` : `${v} ${currency}`
}

/** A purchase belongs to the visit it was paid during. Close by (twice the
 *  visit's radius, at least 250 m) and at most five minutes outside the
 *  stay; or — since a shop's pin comes from its address and can be off by
 *  a street or two — up to 1.5 km away when it was paid well inside the stay. */
export function purchasesByVisit(visits: Visit[], purchases: Purchase[]): Map<number, Purchase[]> {
  const out = new Map<number, Purchase[]>()
  for (const p of purchases) {
    if (!p.time_known) continue
    let best: Visit | null = null
    let bestScore = Infinity
    for (const v of visits) {
      if (v.status === 'declined' || p.ts < v.start_ts - 300 || p.ts > v.end_ts + 300) continue
      const d = haversine([v.lat, v.lng], [p.lat, p.lng])
      const near = d <= Math.max(v.radius * 2, 250)
      const inside = p.ts >= v.start_ts && p.ts <= v.end_ts
      if (!near && !(inside && d <= 1500)) continue
      const score = near ? d : 10000 + d // a close visit beats a merely simultaneous one
      if (score < bestScore) { best = v; bestScore = score }
    }
    if (best) out.set(best.id, [...(out.get(best.id) || []), p])
  }
  return out
}

/** Whether a visit's own name should give way to the shop's: not when the
 *  person named it or it is one of their saved places. */
export const shopNamesVisit = (v: Visit) => !v.own_name && v.place?.kind !== 'saved'
