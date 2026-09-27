// REST calls, and the join credentials this browser keeps per session.
import type { Role } from "./types";

export interface Seat {
  session: string;
  name: string;
  user: string;
  role: Role;
  token: string;
  invite_code?: string;
}

async function call<T>(path: string, init: RequestInit): Promise<T> {
  const res = await fetch(path, init);
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error ?? body.message ?? `request failed (${res.status})`);
  return body as T;
}

function postJSON<T>(path: string, body: unknown): Promise<T> {
  return call<T>(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
}

export function createSession(name: string, displayName: string): Promise<Seat> {
  return postJSON("/api/sessions", { name, display_name: displayName });
}

export function joinSession(session: string, code: string, displayName: string): Promise<Seat> {
  return postJSON(`/api/sessions/${encodeURIComponent(session)}/join`, { code, display_name: displayName });
}

export interface Grid {
  cols: number;
  rows: number;
  cellFeet: number;
}

export function uploadMap(seat: Seat, image: File, grid: Grid): Promise<{ image_url: string }> {
  const form = new FormData();
  form.set("image", image);
  form.set("cols", String(grid.cols));
  form.set("rows", String(grid.rows));
  form.set("cell_feet", String(grid.cellFeet));
  return call(`/api/sessions/${encodeURIComponent(seat.session)}/maps`, {
    method: "POST",
    headers: { Authorization: `Bearer ${seat.token}` },
    body: form,
  });
}

// uploadImage stores a picture for a token and returns its URL.
export function uploadImage(seat: Seat, image: File): Promise<{ image_url: string }> {
  const form = new FormData();
  form.set("image", image);
  return call(`/api/sessions/${encodeURIComponent(seat.session)}/images`, {
    method: "POST",
    headers: { Authorization: `Bearer ${seat.token}` },
    body: form,
  });
}

// deleteSession removes the session for everyone. DM only.
export async function deleteSession(seat: Seat): Promise<void> {
  const res = await fetch(`/api/sessions/${encodeURIComponent(seat.session)}`, {
    method: "DELETE",
    headers: { Authorization: `Bearer ${seat.token}` },
  });
  if (!res.ok && res.status !== 404) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error ?? `request failed (${res.status})`);
  }
}

// Seats are the only identity there is (no accounts), so they live in
// localStorage. Losing one means rejoining through the invite link.
const SEATS_KEY = "yorm.seats";
const memorySeats: Record<string, Seat> = {}; // fallback when storage is unavailable

export function loadSeats(): Record<string, Seat> {
  let stored: Record<string, Seat> = {};
  try {
    stored = JSON.parse(localStorage.getItem(SEATS_KEY) ?? "{}") as Record<string, Seat>;
  } catch {
    // Private mode, blocked storage, or corrupt data.
  }
  return { ...stored, ...memorySeats };
}

export function saveSeat(seat: Seat): void {
  memorySeats[seat.session] = seat;
  try {
    localStorage.setItem(SEATS_KEY, JSON.stringify(loadSeats()));
  } catch {
    // The seat lasts until the tab closes.
  }
}

// forgetSeat removes a session from this browser's list.
export function forgetSeat(session: string): void {
  delete memorySeats[session];
  try {
    const stored = JSON.parse(localStorage.getItem(SEATS_KEY) ?? "{}") as Record<string, Seat>;
    delete stored[session];
    localStorage.setItem(SEATS_KEY, JSON.stringify(stored));
  } catch {
    // Nothing stored to forget.
  }
}

export function inviteLink(seat: Seat): string | undefined {
  if (!seat.invite_code) return undefined;
  // The code rides in the fragment, which browsers never send to servers.
  return `${location.origin}/join/${seat.session}#${seat.invite_code}`;
}
