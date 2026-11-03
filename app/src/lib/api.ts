import { invoke } from "@tauri-apps/api/core";

export const PERMS = ["view", "download", "upload", "mkdir", "overwrite", "rename", "delete", "deleteown"] as const;
export type Perm = (typeof PERMS)[number];

export const PERM_LABEL: Record<Perm, string> = {
  view: "See files",
  download: "Download",
  upload: "Upload",
  mkdir: "New folders",
  overwrite: "Replace files",
  rename: "Rename / move",
  delete: "Delete anything",
  deleteown: "Delete own uploads",
};

// Mirrors perm.Presets on the server.
export const PRESETS: Record<string, Perm[]> = {
  viewer: ["view", "download"],
  contributor: ["view", "download", "upload", "mkdir"],
  editor: ["view", "download", "upload", "mkdir", "overwrite", "rename", "delete"],
  dropbox: ["upload"],
};

export const PRESET_HELP: Record<string, string> = {
  viewer: "see and download",
  contributor: "also upload and make folders",
  editor: "full control",
  dropbox: "upload only, can't see anything",
};

export interface Tailnet {
  running: boolean;
  backendState: string;
  login: string;
  userId: number;
  node: string;
  tailnet: string;
  peers: { dns: string; host: string; online: boolean; owner: string; tagged: boolean }[];
}

export interface User {
  id: number;
  login: string;
  display: string;
  node: string;
  external: boolean;
}

export interface ShareInfo {
  name: string;
  perms: Perm[];
  used: number;
  quota?: number;
  expires?: string;
  owner?: boolean;
}

export interface Me {
  user: User;
  admin: boolean;
  shares: ShareInfo[];
}

export interface ShareConfig {
  name: string;
  path: string;
  readOnly?: boolean;
  access: Record<string, Perm[] | string>;
  expires?: Record<string, string>;
  quotaGB?: Record<string, number>;
}

export interface Config {
  admins: string[];
  allowExternalAdmins?: boolean;
  minFreeGB?: number;
  shares: ShareConfig[];
}

export interface PeerStatus {
  userId: number;
  login: string;
  node: string;
  online: boolean;
  external: boolean;
  canReach: boolean;
  hasAccess: boolean;
}

export interface AdminStatus {
  shares: { name: string; path: string; readOnly: boolean; online: boolean; freeBytes: number; trashCount: number }[];
  peers: PeerStatus[];
  policyKnown: boolean;
  grant?: string;
  minFreeBytes: number;
}

export interface ActivityEntry {
  time: string;
  userId: number;
  login: string;
  node: string;
  external?: boolean;
  action: string;
  share: string;
  path?: string;
  to?: string;
  bytes?: number;
  seconds?: number;
  note?: string;
}

export interface TrashItem {
  id: string;
  path: string;
  by: string;
  at: string;
  dir: boolean;
  size: number;
}

export interface Browse {
  path: string;
  parent?: string;
  dirs: string[];
}

export type Phase = "stopped" | "starting" | "login" | "approval" | "running" | "failed";
export interface HostSnapshot {
  state: HostState;
  log: string[];
  background: boolean;
  backgroundSupported: boolean;
  registered: boolean;
}

export interface HostState {
  phase: Phase;
  url?: string;
  loginUrl?: string;
  notice?: string;
  warning?: string;
  error?: string;
  hostname: string;
  stateDir: string;
}

export interface Settings {
  hostOnLaunch: boolean;
  background: boolean;
  hostname: string;
  roots: string[];
  servers: string[];
  drives: DriveSpec[];
  cacheGb: number;
}

export interface DriveSpec {
  base: string;
  share: string;
  target: string;
  readOnly: boolean;
  auto: boolean;
}

export interface Queued {
  name: string;
  size: number;
  uploading: boolean;
  startsIn: number;
  tries: number;
  error?: string;
}

export interface Mounted {
  remote: string;
  target: string;
  cacheBytes: number;
  uploadsQueued: number;
  uploadsInProgress: number;
  erroredFiles: number;
  queue: Queued[];
}

export interface Transfer {
  name: string;
  remote: string;
  upload: boolean;
  size: number;
  bytes: number;
  speed: number;
  eta?: number;
}

export interface DrivesState {
  running: boolean;
  missing?: "winfsp" | "fuse";
  mounts: Mounted[];
  transfers: Transfer[];
  log: string[];
  names: Record<string, string>;
}

/// A share can be a drive only if you can read what's in it.
export const mountable = (perms: Perm[]) => perms.includes("view") && perms.includes("download");
const WRITE: Perm[] = ["upload", "mkdir", "overwrite", "rename", "delete", "deleteown"];
export const canWrite = (perms: Perm[]) => WRITE.some((p) => perms.includes(p));

export function pendingUploads(d: DrivesState): number {
  return d.mounts.reduce((n, m) => n + Math.max(m.queue.length, m.uploadsQueued + m.uploadsInProgress), 0);
}

export class ApiError extends Error {
  constructor(
    message: string,
    public status: number,
  ) {
    super(message);
  }
}

interface RawResponse {
  status: number;
  body: string;
  etag?: string;
}

export async function call<T>(
  base: string,
  method: string,
  path: string,
  body?: unknown,
  ifMatch?: string,
): Promise<{ data: T; etag?: string }> {
  let r: RawResponse;
  try {
    r = await invoke<RawResponse>("api_request", {
      req: { base, method, path, body: body === undefined ? null : JSON.stringify(body), ifMatch: ifMatch ?? null },
    });
  } catch (e) {
    throw new ApiError(String(e), 0);
  }
  let data: any = null;
  if (r.body) {
    try {
      data = JSON.parse(r.body);
    } catch {
      data = r.body;
    }
  }
  if (r.status >= 400) {
    throw new ApiError((data && data.error) || `HTTP ${r.status}`, r.status);
  }
  return { data: data as T, etag: r.etag };
}

export const get = async <T>(base: string, path: string) => (await call<T>(base, "GET", path)).data;

export function baseOf(url: string): string {
  const u = new URL(url);
  return `${u.protocol}//${u.host}/`;
}

export function shortName(base: string): string {
  const host = new URL(base).hostname;
  return host.endsWith(".ts.net") ? host.split(".")[0] : new URL(base).host;
}

export function fmtSize(n: number): string {
  if (n < 1000) return Math.round(n) + " B";
  const u = ["KB", "MB", "GB", "TB"];
  let i = -1;
  do {
    n /= 1000;
    i++;
  } while (n >= 1000 && i < u.length - 1);
  return n.toFixed(n < 10 ? 1 : 0) + " " + u[i];
}

export function fmtDate(s: string): string {
  const d = new Date(s);
  return d.toLocaleDateString() + " " + d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

export function presetOf(perms: Perm[]): string | null {
  const key = [...perms].sort().join(",");
  return Object.keys(PRESETS).find((k) => [...PRESETS[k]].sort().join(",") === key) ?? null;
}

export function permsOf(v: Perm[] | string): Perm[] {
  if (typeof v === "string") return [...(PRESETS[v.toLowerCase()] ?? [])];
  return v.map((p) => p.toLowerCase() as Perm);
}

export function describePerms(perms: Perm[]): string {
  const preset = Object.entries(PRESETS)
    .filter(([, ps]) => ps.every((p) => perms.includes(p)))
    .sort((a, b) => b[1].length - a[1].length)[0];
  const extra = perms.filter((p) => !preset || !preset[1].includes(p)).map((p) => PERM_LABEL[p].toLowerCase());
  return [preset?.[0], ...extra].filter(Boolean).join(" + ");
}
