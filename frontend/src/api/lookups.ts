// Small master lists used in pickers (cities, clusters, properties, categories, ...), loaded once and shared.
import { useEffect, useState } from 'react'
import { get } from './client'
import type { Property } from './types'

export interface Lookups {
  cities: { id: number; name: string; state_code: string; active: boolean }[]
  clusters: { id: number; name: string; city_id: number; city: string; manager_id: number | null; manager_name: string; active: boolean }[]
  properties: Property[]
  categories: { id: number; name: string; is_fb: boolean }[]
  subCategories: { id: number; name: string; code: string; category_id: number; category: string; is_fb: boolean; active: boolean }[]
  brands: { id: number; name: string; active: boolean }[]
  packSizes: { id: number; name: string; active: boolean }[]
  billTypes: { id: number; name: string; section: string; default_gst: number; active: boolean }[]
  people: { id: number; name: string; active: boolean; steps: string[] }[]
  roles: { id: number; code: string; name: string; step: string; rights: string[]; is_custom: boolean; active: boolean; users: number }[]
}

let cache: Lookups | null = null
// Bill types open with bills in release phase 3; before that they are not asked for. Set from the signed-in user.
let billsOpen = true
export function setBillsOpen(open: boolean) { billsOpen = open }
let pending: Promise<Lookups> | null = null
const listeners = new Set<(l: Lookups) => void>()

async function load(): Promise<Lookups> {
  const [cities, clusters, properties, categories, subCategories, brands, packSizes, billTypes, people, roles] = await Promise.all([
    get('/cities'), get('/clusters'), get('/properties'), get('/categories'), get('/sub-categories'), get('/brands'), get('/pack-sizes'), billsOpen ? get('/bill-types').catch(() => []) : Promise.resolve([]), get('/people'), get('/roles'),
  ])
  return { cities, clusters, properties, categories, subCategories, brands, packSizes, billTypes, people, roles }
}

/** Reloads the lists after a master changes. */
export function refreshLookups() {
  pending = load().then((l) => { cache = l; listeners.forEach((f) => f(l)); return l })
  return pending
}

export function useLookups(): Lookups | null {
  const [l, setL] = useState<Lookups | null>(cache)
  useEffect(() => {
    listeners.add(setL)
    if (!cache && !pending) refreshLookups()
    else if (pending && !cache) pending.then(setL)
    return () => { listeners.delete(setL) }
  }, [])
  return l
}
