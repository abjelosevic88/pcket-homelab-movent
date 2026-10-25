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

/** A purchase belongs to the visit it was paid during: within ten minutes of
 *  its stay, and close to it (twice its radius, at least 250 m). */
export function purchasesByVisit(visits: Visit[], purchases: Purchase[]): Map<number, Purchase[]> {
  const out = new Map<number, Purchase[]>()
  for (const p of purchases) {
    if (!p.time_known) continue
    let best: Visit | null = null
    let bestD = Infinity
    for (const v of visits) {
      if (v.status === 'declined' || p.ts < v.start_ts - 600 || p.ts > v.end_ts + 600) continue
      const d = haversine([v.lat, v.lng], [p.lat, p.lng])
      if (d <= Math.max(v.radius * 2, 250) && d < bestD) { best = v; bestD = d }
    }
    if (best) out.set(best.id, [...(out.get(best.id) || []), p])
  }
  return out
}
