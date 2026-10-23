export interface User { id: number; email: string; name: string; role: 'admin' | 'member'; created_at: string }
export interface APIToken { id: number; name: string; prefix: string; last_used_at: string | null; created_at: string }

export interface Settings {
  title: string; setup_complete: boolean
  timezone: string; distance_unit: 'km' | 'mi'; map_tiles: string; route_color: 'speed' | 'mode' | 'plain'
  visit_radius: number; visit_min_minutes: number; track_gap_minutes: number; max_accuracy: number; max_speed_kmh: number
  modes_off: string[]; mode_speeds: ModeSpeeds
  notify_stale_hours: number; notify_countries: boolean
}

/** km/h. "Top" is what a journey stays under 85% of the time, "typical" its median. */
export interface ModeSpeeds {
  walk_top: number; walk_typical: number; run_top: number; run_typical_min: number; run_typical_max: number
  cycle_top: number; cycle_typical: number; bus_avg: number; bus_top: number; train_top: number; boat_top: number; fly_top: number
  ride_from: number
}

export interface Device {
  id: number; user_id: number; name: string; kind: string; scope: 'track' | 'read' | 'full'; api_key?: string; tracker_id: string
  last_seen_at: number; last_point_at: number; last_lat: number | null; last_lng: number | null; last_battery: number | null; last_battery_state: string
  last_ip: string; user_agent: string; points: number; revoked: boolean; created_at: number
}
export interface SetupField { label: string; value: string; copy: boolean }
export interface Setup { kind: string; title: string; fields: SetupField[]; steps: string[]; qr?: string; link?: string; code?: Record<string, string>; notes?: string[] }

export interface Place {
  id: number; name: string; kind: 'saved' | 'discovered'; category: string; color: string; lat: number; lng: number; radius: number
  address: string; city: string; country: string; country_name: string; geocoded: number; renamed: boolean; hidden: boolean; notes: string
  visits: number; total_s: number; first_visit: number; last_visit: number
}

export type VisitStatus = 'suggested' | 'confirmed' | 'declined'
export interface Visit {
  id: number; start_ts: number; end_ts: number; lat: number; lng: number; radius: number; points: number; status: VisitStatus
  name: string; own_name: string; notes: string; manual: boolean; place_id: number | null; place: Place | null
}

export interface Journey {
  id: number; start_ts: number; end_ts: number; distance: number; points: number; avg_speed: number; max_speed: number
  elevation_gain: number; elevation_loss: number; mode: string; mode_manual: boolean; start: [number, number]; end: [number, number]
  polyline: string; countries: string[]
}

export type Entry = (Visit & { type: 'visit' }) | (Journey & { type: 'journey' })

export interface DaySummary {
  date: string; points: number; distance: number; moving_s: number; first_ts: number; last_ts: number; countries: string[]
  modes: Record<string, number>; visits: number; places: number[]; max_from_home: number
}

export interface DayView { date: string; from: number; to: number; summary: DaySummary | null; entries: Entry[]; prev: string; next: string; unit: string; pending: boolean }

export interface MapData {
  from: number; to: number; tracks: Journey[]; visits: Visit[]; raw: string; pending: boolean; distance: number
  modes: Record<string, number>; bounds: [number, number, number, number] | null
}

export interface Share {
  id: number; viewer_id: number | null; viewer_name: string; viewer_email: string; token: string; name: string; level: 'live' | 'history'
  precision_m: number; expires_at: number; created_at: number; expired: boolean; link: boolean; url?: string
}
export interface SharedWithMe { id: number; owner_id: number; owner_name: string; owner_email: string; level: string; precision_m: number; expires_at: number }
export interface LivePerson {
  user_id: number; name: string; email?: string; level: string; precision_m: number; expires_at: number
  point: { lat: number; lng: number; ts: number; battery: number | null; battery_state: string; accuracy: number; country: string } | null
  place: { name: string; since: number } | null
}

export interface PrivacyZone { id: number; name: string; lat: number; lng: number; radius: number; drop_points: boolean; created_at: number }

export interface DawarichLink {
  url: string; account: string; source: 'direct' | 'core'; enabled: boolean; cursor_ts: number; last_sync_at: number; last_added: number; points_added: number
  last_error: string; created_at: number
}

export interface Import {
  id: number; name: string; format: string; status: 'queued' | 'running' | 'done' | 'failed'; size: number; source_url?: string
  points_read: number; points_added: number; visits_added: number; first_ts: number; last_ts: number; error: string; progress: number
  created_at: number; finished_at: number
}

export interface Bootstrap {
  user: User; settings: Settings; timezone: string; version: string; base_url: string
  points: number; first_ts: number; last_ts: number; first_day: string; last_day: string
  devices: Device[]; home: Place | null; shared_with_me: SharedWithMe[]; pending: boolean; geocoder: boolean
  core: { configured: boolean; registered: boolean; error: string; url: string }; modes: string[]
}

export interface NowView {
  point: { lat: number; lng: number; ts: number; accuracy: number | null; speed: number | null; battery: number | null; battery_state: string; country: string; country_name: string; source: string } | null
  visit: Visit | null; journey: Journey | null; devices: Device[]; today?: DaySummary
}

export interface CountryRow { code: string; name: string; continent: string; days: number; first: string; last: string; years?: number[]; new?: boolean }
export interface CityRow { city: string; country: string; visits: number; first: number; last: number; total_s: number }

export interface Insights {
  from: string; to: string; unit: string
  totals: { distance: number; days: number; points: number; moving_s: number; journeys: number; visits: number; places: number; new_places: number; countries: number; cities: number }
  months: { month: string; distance: number; days: number; countries: number; moving_s: number; visits: number }[]
  modes: Record<string, number>; journeys_by_mode: Record<string, number>
  weekdays: { weekday: string; distance: number; days: number; avg: number }[]
  countries: CountryRow[]; cities: CityRow[]
  top_places: { place: Place; visits: number; total_s: number }[]
  records: {
    longest_day?: { date: string; distance: number }; furthest_from_home?: { date: string; distance: number }
    most_countries?: { date: string; countries: string[] }; longest_journey?: Journey; fastest_journey?: Journey; longest_streak?: { days: number; ended: string }
  }
  time_split: { home: number; work: number; elsewhere: number; moving: number; unknown: number }
}
