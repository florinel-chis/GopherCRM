/**
 * The stored web session (#184): one JSON record under `gophercrm_session`,
 * in localStorage for a remembered session and in sessionStorage otherwise.
 *
 * The record carries both tokens, the account and the persistence choice
 * together, so a session is always read whole from one storage and an access
 * token can never be paired with a refresh token of another session. The two
 * legacy keys are still written after every record write, as write-only
 * mirrors: the protected browser specs read `gophercrm_token` and sign out by
 * removing it, so a record is honoured only while its mirror exists.
 */

export const SESSION_KEY = 'gophercrm_session';
export const TOKEN_KEY = 'gophercrm_token';
export const REFRESH_TOKEN_KEY = 'gophercrm_refresh_token';

export interface SessionRecord {
  v: 1;
  /** Session id: random for a sign-in, `legacy-<sha256>` for a migrated session. */
  id: string;
  access: string;
  refresh: string | null;
  /** The unverified `user_id` claim of the access token at sign-in. */
  subject: string | null;
  persist: boolean;
}

/** 32 lowercase hex characters from crypto.getRandomValues. */
export function newSessionId(): string {
  const bytes = new Uint8Array(16);
  globalThis.crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}

/**
 * Parses a stored record. Anything that is not valid JSON, has `v !== 1`, or
 * lacks a string id or a string access token is malformed (null).
 */
export function parseSessionRecord(raw: string): SessionRecord | null {
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    return null;
  }
  if (typeof value !== 'object' || value === null) return null;
  const candidate = value as Record<string, unknown>;
  if (candidate.v !== 1 || typeof candidate.id !== 'string' || typeof candidate.access !== 'string') {
    return null;
  }
  return {
    v: 1,
    id: candidate.id,
    access: candidate.access,
    refresh: typeof candidate.refresh === 'string' ? candidate.refresh : null,
    subject: typeof candidate.subject === 'string' ? candidate.subject : null,
    persist: candidate.persist === true,
  };
}

export function serializeSessionRecord(record: SessionRecord): string {
  return JSON.stringify({
    v: 1,
    id: record.id,
    access: record.access,
    refresh: record.refresh,
    subject: record.subject,
    persist: record.persist,
  });
}

/**
 * The id of a session migrated from the legacy two-key layout. It is derived,
 * not random, so two tabs migrating the same session concurrently agree, and
 * it is a digest, so the id never contains the token itself.
 */
export function legacySessionId(access: string, refresh: string | null): string {
  return `legacy-${sha256Hex(refresh ?? access)}`;
}

const K = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);

const rotr = (x: number, n: number): number => (x >>> n) | (x << (32 - n));

const utf8 = (text: string): Uint8Array => {
  const out: number[] = [];
  for (const char of text) {
    const cp = char.codePointAt(0) ?? 0;
    if (cp < 0x80) {
      out.push(cp);
    } else if (cp < 0x800) {
      out.push(0xc0 | (cp >> 6), 0x80 | (cp & 0x3f));
    } else if (cp < 0x10000) {
      out.push(0xe0 | (cp >> 12), 0x80 | ((cp >> 6) & 0x3f), 0x80 | (cp & 0x3f));
    } else {
      out.push(0xf0 | (cp >> 18), 0x80 | ((cp >> 12) & 0x3f), 0x80 | ((cp >> 6) & 0x3f), 0x80 | (cp & 0x3f));
    }
  }
  return Uint8Array.from(out);
};

/**
 * Lowercase hex SHA-256 of the UTF-8 encoding of `text`, computed
 * synchronously: crypto.subtle is asynchronous and missing in non-secure
 * contexts, and the session must be readable synchronously.
 */
export function sha256Hex(text: string): string {
  const bytes = utf8(text);
  const paddedLength = ((bytes.length + 9 + 63) >> 6) << 6;
  const data = new Uint8Array(paddedLength);
  data.set(bytes);
  data[bytes.length] = 0x80;
  const view = new DataView(data.buffer);
  const bitLength = bytes.length * 8;
  view.setUint32(paddedLength - 8, Math.floor(bitLength / 0x100000000));
  view.setUint32(paddedLength - 4, bitLength >>> 0);

  const h = new Uint32Array([
    0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
  ]);
  const w = new Uint32Array(64);
  for (let offset = 0; offset < paddedLength; offset += 64) {
    for (let i = 0; i < 16; i++) {
      w[i] = view.getUint32(offset + i * 4);
    }
    for (let i = 16; i < 64; i++) {
      const s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^ (w[i - 15] >>> 3);
      const s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^ (w[i - 2] >>> 10);
      w[i] = (w[i - 16] + s0 + w[i - 7] + s1) | 0;
    }
    let a = h[0];
    let b = h[1];
    let c = h[2];
    let d = h[3];
    let e = h[4];
    let f = h[5];
    let g = h[6];
    let hh = h[7];
    for (let i = 0; i < 64; i++) {
      const S1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25);
      const ch = (e & f) ^ (~e & g);
      const t1 = (hh + S1 + ch + K[i] + w[i]) | 0;
      const S0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22);
      const maj = (a & b) ^ (a & c) ^ (b & c);
      const t2 = (S0 + maj) | 0;
      hh = g;
      g = f;
      f = e;
      e = (d + t1) | 0;
      d = c;
      c = b;
      b = a;
      a = (t1 + t2) | 0;
    }
    h[0] += a;
    h[1] += b;
    h[2] += c;
    h[3] += d;
    h[4] += e;
    h[5] += f;
    h[6] += g;
    h[7] += hh;
  }
  return Array.from(h, (x) => x.toString(16).padStart(8, '0')).join('');
}
