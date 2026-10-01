// D16: 引入类型化的服务器信息/网络响应接口（与 Win 端对齐）
import type { ServerInfoResponse, ServerNetworkResponse, PortInfo, SecurityQuestion, Whiteboard, WhiteboardStroke, UserSound, UserSoundSettings } from './types';

// API_BASE is held inside a const object so the binding is immutable while
// the value remains mutable through setApiBase. This avoids a top-level let
// and makes the mutable state explicit. For tests that need isolation,
// createApiBaseState() returns a fresh state object.
const API_BASE_STATE = { value: '' };

export function createApiBaseState() {
  return { value: '' };
}

export function setApiBase(base: string) {
  API_BASE_STATE.value = base.replace(/\/$/, '');
}

// 测试辅助：Vitest 环境下将 api-core 的可变全局状态（API base、token 键、
// client 类型等）重置为出厂值，避免跨测试文件污染。生产代码不使用。
export function resetApiCoreForTests() {
  API_BASE_STATE.value = '';
  TOKEN_KEY = 'rrt_token';
  tokenGetter = null;
  CLIENT_TYPE = 'web';
  DEVICE_NAME = '';
  refreshingPromise = null;
}

export function getApiBase(): string {
  return API_BASE_STATE.value;
}

let TOKEN_KEY = 'rrt_token';
export function setTokenKey(key: string) { TOKEN_KEY = key; }

// Optional override to read the access token from an in-memory store instead of
// shared localStorage. This is used by the web client so that multiple tabs in
// the same browser profile do not accidentally share authentication state during
// tests, and so that the auth store remains the single source of truth.
let tokenGetter: (() => string | null) | null = null;
export function setAccessTokenGetter(fn: () => string | null) { tokenGetter = fn; }

// C33: 客户端类型标识（web | desktop），桌面客户端通过 setClientType('desktop') 设置
let CLIENT_TYPE: 'web' | 'desktop' = 'web';
export function setClientType(type: 'web' | 'desktop') { CLIENT_TYPE = type; }

// ── S-2 多设备标识 ──────────────────────────────────────────────
// 登录 / refresh 时随请求上报的设备信息，写入服务端会话行（user_sessions），
// 供「登录设备」列表展示。deviceName 由桌面客户端启动时通过 setDeviceName()
// 注入（Electron 主进程 os.hostname()）；web 端无注入时回退为 UA 短描述。

const MAX_DEVICE_TYPE_LEN = 32;
const MAX_DEVICE_NAME_LEN = 64;

let DEVICE_NAME = '';

/** 注入本机设备名（桌面客户端 main.tsx 启动时调用；重复调用以最后一次为准） */
export function setDeviceName(name: string): void {
  DEVICE_NAME = (name || '').trim();
}

/** 按字符（rune）截断，与服务端 sanitizeSessionDevice 的上限对齐 */
function truncateRunes(s: string, max: number): string {
  const runes = Array.from(s);
  return runes.length > max ? runes.slice(0, max).join('') : s;
}

/** 组装随 login/refresh 请求上报的设备字段 */
export function getClientDeviceInfo(): { deviceType: string; deviceName: string } {
  const deviceType = CLIENT_TYPE === 'desktop' ? 'windows-desktop' : 'web';
  let deviceName = DEVICE_NAME;
  if (!deviceName && typeof navigator !== 'undefined') {
    try {
      deviceName = navigator.userAgent || '';
    } catch {
      deviceName = '';
    }
  }
  return {
    deviceType: truncateRunes(deviceType, MAX_DEVICE_TYPE_LEN),
    deviceName: truncateRunes(deviceName, MAX_DEVICE_NAME_LEN),
  };
}

let refreshingPromise: Promise<string> | null = null;

function getAccessToken(): string | null {
  if (tokenGetter) {
    try {
      return tokenGetter();
    } catch {
      // fall through to localStorage
    }
  }
  return localStorage.getItem(TOKEN_KEY);
}

function clearStoredAuthTokens(): void {
  localStorage.removeItem(TOKEN_KEY);
  localStorage.removeItem('rrt_refresh_token');
}

async function refreshAccessToken(): Promise<string> {
  if (refreshingPromise) return refreshingPromise;

  refreshingPromise = (async () => {
    try {
      const refreshToken = localStorage.getItem('rrt_refresh_token');
      if (!refreshToken) throw new Error('No refresh token');

      const res = await fetch(`${API_BASE_STATE.value}/api/auth/refresh`, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        // S-2 多设备标识：refresh 同步上报设备信息（服务端轮换会话行时更新设备字段）
        body: JSON.stringify({ refreshToken, ...getClientDeviceInfo() }),
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok || data.code !== 'OK') {
        throw new Error(data.chinese_message || data.message || 'Refresh failed');
      }

      const newToken = data.data.accessToken;
      const newRefreshToken = data.data.refreshToken;
      localStorage.setItem(TOKEN_KEY, newToken);
      if (newRefreshToken) {
        localStorage.setItem('rrt_refresh_token', newRefreshToken);
      }
      return newToken;
    } finally {
      refreshingPromise = null;
    }
  })();

  return refreshingPromise;
}

function getCSRFToken(): string {
  // Read csrf_token cookie and generate HMAC token
  const match = document.cookie.match(/csrf_token=([^;]+)/);
  if (!match) return '';
  // The backend expects the raw session ID as csrf_token for simple validation
  // Or we need to compute HMAC. Since we don't have the secret, we send the session ID
  // and let the backend validate it.
  return match[1];
}

// 当 token 被清除（过期/失效）时通知应用层，触发自动登出并跳转到登录页。
// 使用全局事件避免循环依赖（api-core 不应直接依赖 authStore）。
function notifyAuthExpired(): void {
  try {
    if (typeof window !== 'undefined' && window.dispatchEvent) {
      window.dispatchEvent(new CustomEvent('rrt:auth-expired'));
    }
  } catch {
    // 事件派发失败不影响主流程
  }
}

async function api<T>(path: string, method: string = 'GET', body?: unknown): Promise<T> {
  const doRequest = async (tokenOverride?: string): Promise<T> => {
    const token = tokenOverride || getAccessToken();
    const headers: Record<string, string> = {};
    const isFormData = typeof FormData !== 'undefined' && body instanceof FormData;
    if (!isFormData) {
      headers['Content-Type'] = 'application/json';
    }
    if (token) headers['Authorization'] = `Bearer ${token}`;
    // C33: 桌面客户端标识（用于屏幕共享等 Web 端限制场景）
    if (CLIENT_TYPE) headers['X-Client-Type'] = CLIENT_TYPE;
    // Always add CSRF token for non-GET requests
    if (method !== 'GET') {
      const csrf = getCSRFToken();
      if (csrf) headers['X-CSRF-Token'] = csrf;
    }

    const res = await fetch(`${API_BASE_STATE.value}${path}`, {
      method,
      credentials: 'include',
      headers,
      ...(body ? { body: isFormData ? body as FormData : JSON.stringify(body) } : {}),
    });
    const data = await res.json().catch(() => ({}));

    if (data.code === 'AUTH_TOKEN_EXPIRED') {
      try {
        const newToken = await refreshAccessToken();
        return doRequest(newToken);
      } catch {
        clearStoredAuthTokens();
        notifyAuthExpired();
        throw new Error('登录已过期，请重新登录');
      }
    }

    if (!res.ok || data.code === 'AUTH_TOKEN_MISSING' || data.code === 'AUTH_TOKEN_INVALID' || data.code === 'AUTH_UNAUTHORIZED') {
      if (data.code === 'AUTH_TOKEN_MISSING' || data.code === 'AUTH_TOKEN_INVALID' || data.code === 'AUTH_UNAUTHORIZED') {
        clearStoredAuthTokens();
        notifyAuthExpired();
      }
      const err = new Error(data.chinese_message || data.message || `HTTP ${res.status}`);
      // 附加错误码和详情，便于调用方区分错误类型和获取具体校验失败原因
      if (data.code) (err as any).code = data.code;
      if (data.details) (err as any).details = data.details;
      throw err;
    }

    return data as T;
  };

  return doRequest();
}

export async function apiGet<T>(path: string): Promise<T> {
  return api<T>(path, 'GET');
}

export async function apiPost<T>(path: string, body?: unknown): Promise<T> {
  return api<T>(path, 'POST', body);
}

export async function apiPatch<T>(path: string, body?: unknown): Promise<T> {
  return api<T>(path, 'PATCH', body);
}

export async function apiPut<T>(path: string, body?: unknown): Promise<T> {
  return api<T>(path, 'PUT', body);
}

export async function apiDelete<T>(path: string, body?: unknown): Promise<T> {
  return api<T>(path, 'DELETE', body);
}

// D14: XHR 上传，支持 onProgress 进度回调与 token 失败重试。
// 保留 Web 版的 CSRF、可配置 TOKEN_KEY、CLIENT_TYPE 与 chinese_message 错误信息。
export async function upload<T>(url: string, formData: FormData, onProgress?: (loaded: number, total: number) => void, signal?: AbortSignal): Promise<T> {
  const doUpload = (tokenOverride?: string): Promise<T> => {
    return new Promise<T>((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open('POST', API_BASE_STATE.value + url);
      xhr.withCredentials = true;
      const token = tokenOverride || getAccessToken();
      if (token) xhr.setRequestHeader('Authorization', `Bearer ${token}`);
      // C33: 客户端类型标识
      if (CLIENT_TYPE) xhr.setRequestHeader('X-Client-Type', CLIENT_TYPE);
      // CSRF token for non-GET (POST here)
      const csrf = getCSRFToken();
      if (csrf) xhr.setRequestHeader('X-CSRF-Token', csrf);
      if (onProgress) {
        xhr.upload.onprogress = (e: ProgressEvent) => {
          if (e.lengthComputable) {
            onProgress(e.loaded, e.total);
          }
        };
      }
      let onAbort: (() => void) | null = null;
      if (signal) {
        if (signal.aborted) {
          reject(new Error('上传已取消'));
          return;
        }
        onAbort = () => {
          xhr.abort();
          reject(new Error('上传已取消'));
        };
        signal.addEventListener('abort', onAbort);
      }
      const cleanup = () => {
        if (onAbort) signal?.removeEventListener('abort', onAbort);
      };
      xhr.onload = () => {
        cleanup();
        let data: any = null;
        try { data = xhr.responseText ? JSON.parse(xhr.responseText) : null; } catch {}
        const code = data?.code as string | undefined;
        // Token expired: refresh and retry once
        if (code === 'AUTH_TOKEN_EXPIRED') {
          refreshAccessToken().then(newToken => {
            doUpload(newToken).then(resolve, reject);
          }).catch(() => {
            clearStoredAuthTokens();
            reject(new Error('登录已过期，请重新登录'));
          });
          return;
        }
        // Other auth errors: clear tokens
        if (code === 'AUTH_TOKEN_MISSING' || code === 'AUTH_TOKEN_INVALID' || code === 'AUTH_UNAUTHORIZED') {
          clearStoredAuthTokens();
        }
        if (xhr.status >= 200 && xhr.status < 300) {
          if (data === null) {
            reject(new Error('Invalid JSON response'));
            return;
          }
          resolve(data as T);
          return;
        }
        const err = new Error(data?.chinese_message || data?.message || data?.error || `上传失败: HTTP ${xhr.status}`);
        if (data?.code) (err as any).code = data.code;
        reject(err);
      };
      xhr.onerror = () => { cleanup(); reject(new Error('Network error')); };
      xhr.ontimeout = () => { cleanup(); reject(new Error('Request timeout')); };
      xhr.send(formData);
    });
  };
  return doUpload();
}

export function createWS(path = '/ws'): WebSocket {
  const token = localStorage.getItem(TOKEN_KEY);
  const csrf = getCSRFToken();

  let wsUrl: string;
  if (API_BASE_STATE.value) {
    const url = new URL(API_BASE_STATE.value);
    const protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
    wsUrl = token ? `${protocol}//${url.host}${path}?access_token=${encodeURIComponent(token)}` : `${protocol}//${url.host}${path}`;
  } else {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    wsUrl = token ? `${protocol}//${window.location.host}${path}?access_token=${encodeURIComponent(token)}` : `${protocol}//${window.location.host}${path}`;
  }

  // Append CSRF token so the backend double-submit cookie check passes.
  if (csrf) {
    wsUrl += (wsUrl.includes('?') ? '&' : '?') + `csrf_token=${encodeURIComponent(csrf)}`;
  }

  // C2 fix: Send Sec-WebSocket-Protocol header for backend token parsing.
  // Backend prefers this header over query parameters (see routes.go:381-391).
  // Format: "access_token.<jwt>, rrt" — backend negotiates "rrt" subprotocol.
  // This also fixes the "7 (closed)" error on WS handshake.
  const protocols = token
    ? [`access_token.${token}`, 'rrt']
    : ['rrt'];

  // D15: 鉴权双向兼容 —— 优先 query 参数（浏览器场景，上方已附加 access_token），
  // 同时在连接建立后发送 auth 消息（Win/Electron 场景，query 参数可能被代理剥离）。
  const ws = new WebSocket(wsUrl, protocols);
  ws.onopen = () => {
    if (token) {
      ws.send(JSON.stringify({ type: 'auth', token }));
    }
  };
  return ws;
}

// Server info (D16: 使用 types.ts 中定义的类型化接口)
export const getServerInfo = () => apiGet<ServerInfoResponse>('/api/server/info');
export const getServerNetwork = () => apiGet<ServerNetworkResponse>('/api/server/network');

// ISSUE-081: server-info 的 serverUrl 由服务端 state 的 useHttps 开关决定，可能下发
// 与实际 TLS 终止不一致的协议（典型：https 部署但 useHttps=false → http://…:443，
// nginx 对明文请求直接 400）。客户端不得盲信，需经 resolveServerBaseUrl 校验后再用。

/** 从 nginx 对"明文 HTTP 发往 HTTPS 端口"的 400 HTML 响应中识别协议不匹配 */
export function isPlainHttpToHttpsError(status: number, body: string): boolean {
  return status === 400 && /plain HTTP request was sent to HTTPS port/i.test(body);
}

async function probeServerInfoUrl(url: string, timeoutMs = 3000): Promise<boolean> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const res = await fetch(`${url.replace(/\/+$/, '')}/api/server/info`, {
      method: 'GET',
      credentials: 'omit', // 与 api() 的 include 区分：探测只验证可达性，不带凭据，且避免 CORS 模式差异
      signal: controller.signal,
    });
    if (!res.ok) {
      const body = await res.text().catch(() => '');
      // 可达但 400 + nginx 特征文案 = 协议不匹配，不是有效地址；其余 4xx 视为可达
      return !isPlainHttpToHttpsError(res.status, body);
    }
    try {
      const data = await res.json();
      return typeof data === 'object' && data !== null;
    } catch {
      return false;
    }
  } catch {
    return false;
  } finally {
    clearTimeout(timer);
  }
}

/**
 * 校验 server-info 下发的 serverUrl，返回客户端实际应使用的 API base。
 * 协议与用户输入（当前 API base）不一致时先探测 serverUrl 是否真实可达，
 * 不可达则以用户输入为准；可达性也存疑时返回 null，由调用方提示协议不匹配，
 * 避免静默降级到明文。
 */
export async function resolveServerBaseUrl(serverUrl: string): Promise<string | null> {
  let reported: URL;
  try {
    reported = new URL(serverUrl);
  } catch {
    return null;
  }
  if (reported.protocol !== 'http:' && reported.protocol !== 'https:') return null;
  const current = getApiBase();
  let sameScheme = false;
  try {
    sameScheme = !!current && new URL(current).protocol === reported.protocol;
  } catch {
    sameScheme = false;
  }
  // 协议一致：信任下发地址（canonical URL，原行为），无需探测
  if (sameScheme) return serverUrl;
  // 协议不一致：探测下发地址是否真实可达（nginx 对明文 http→443 返回 400）
  if (await probeServerInfoUrl(serverUrl)) return serverUrl;
  return (await probeServerInfoUrl(current)) ? current : null;
}

// Auth
export const login = (email: string, password: string) =>
  // S-2 多设备标识：登录时上报设备类型/设备名，服务端写入会话行供「登录设备」列表展示
  apiPost<{ code: string; data: { accessToken: string; refreshToken: string; expiresIn: number; user: any } }>('/api/auth/login', { username: email, password, ...getClientDeviceInfo() });
export const register = (username: string, email: string, password: string, securityQuestions: SecurityQuestion[], displayName?: string) =>
  apiPost<{ code: string; data: { accessToken: string; refreshToken: string; expiresIn: number; user: any } }>('/api/auth/register', { username, email, password, displayName, securityQuestions });
export const logout = () => apiPost('/api/auth/logout');
export const adminLogout = () => apiPost('/api/admin/logout');
export const getMe = () => apiGet<{ code: string; data: any }>('/api/auth/me');
export const forgotPassword = (email: string) =>
  apiPost<{ code: string; data: { questions: string[] } }>('/api/auth/forgot-password', { email });

// ── S-2 多设备标识：登录会话管理（列表 / 踢除其他设备） ──────────
export interface AuthSessionItem {
  id: string;
  deviceType: string;
  deviceName: string;
  /** 最近活跃 IP（A4-S1 迁移 000038 起服务端采集；老会话/缺失为空串）。原文未脱敏，展示前须走客户端 desensitizeIp */
  ip?: string;
  lastActiveAt: string;
  createdAt: string;
  expiresAt: string;
  isCurrent: boolean;
}

export const listAuthSessions = () =>
  apiGet<{ code: string; data: { sessions: AuthSessionItem[] } }>('/api/auth/sessions');

/** 下线自己名下的其他设备会话；当前会话会被服务端以 400 拒绝（应走 logout） */
export const revokeAuthSession = (sessionId: string) =>
  apiDelete<{ code: string; data: { message: string } }>(`/api/auth/sessions/${encodeURIComponent(sessionId)}`);

/** A4-S3（DES-20261001-01 §5.2）：一键下线当前用户的其他全部设备。
 * 服务端以 access token 的 session_id claim 圈定「自己」，返回实际下线数量；
 * 老 token 无该 claim 时服务端返回 400 AUTH_NO_SESSION_CONTEXT。 */
export const revokeOtherAuthSessions = () =>
  apiPost<{ code: string; data: { revoked: number } }>('/api/auth/sessions/revoke-others', {});
export const verifySecurityQuestions = (email: string, answers: SecurityQuestion[]) =>
  apiPost<{ code: string; data: { resetToken: string } }>('/api/auth/forgot-password/verify', { email, answers });
export const resetPassword = (token: string, newPassword: string, email?: string) =>
  apiPost<{ code: string; data: any }>('/api/auth/reset-password', { token, newPassword, email });

// User
export const updateProfile = (data: { displayName?: string; customStatus?: string; theme?: string; email?: string }) =>
  apiPatch('/api/users/me', data);

export const uploadAvatar = (userId: string, file: File) => {
  const formData = new FormData();
  formData.append('avatar', file);
  return upload<{ code: string; data: { avatar: string } }>(`/api/users/${userId}/avatar-upload`, formData);
};

// Space
export const getSpace = () => apiGet<{ code: string; data: any }>('/api/space');
export const getMembers = () => apiGet<{ code: string; data: any[] }>('/api/space/members');

// Channels
export const getChannels = () => apiGet<{ code: string; data: any[] }>('/api/channels');
export const createChannel = (data: any) => apiPost('/api/channels', data);
export const updateChannel = (id: string, data: any) => apiPatch(`/api/channels/${id}`, data);
export const deleteChannel = (id: string) => apiDelete(`/api/channels/${id}`);

export function resolveServerAssetUrl(url?: string): string | undefined {
  if (!url || !url.startsWith('/')) return url
  const base = getApiBase()
  return base ? `${base}${url}` : url
}

function normalizeWhiteboard(whiteboard: Whiteboard): Whiteboard {
  if (!whiteboard?.thumbnailUrl) return whiteboard
  return {
    ...whiteboard,
    thumbnailUrl: resolveServerAssetUrl(whiteboard.thumbnailUrl),
  }
}

function withCacheVersion(url: string): string {
  return `${url}${url.includes('?') ? '&' : '?'}v=${Date.now()}`
}

// Messages
// normalizeAttachments 把相对路径的 fileUrl/thumbUrl 拼接为完整 URL 并附加 access_token。
// 浏览器原生 <img>/<video> 标签不会带 Authorization header，需通过 query 参数鉴权。
// 服务端 middleware/auth.go 的 authenticateRequest 支持从 access_token query 读取 token。
function normalizeAttachments(atts: any): any[] {
  if (!Array.isArray(atts) || atts.length === 0) return atts || []
  const base = getApiBase()
  const token = getAccessToken()
  const toFull = (url?: string): string | undefined => {
    if (!url) return url
    if (!url.startsWith('/')) return url // 已是完整 URL，不动
    const full = base ? `${base}${url}` : url
    if (!token) return full
    return full + (full.includes('?') ? '&' : '?') + 'access_token=' + encodeURIComponent(token)
  }
  return atts.map(a => ({
    ...a,
    fileUrl: toFull(a.fileUrl),
    thumbUrl: toFull(a.thumbUrl),
  }))
}

function normalizeMessage(m: any): any {
  if (!m) return m;
  const author = m.author || {};
  return {
    ...m,
    userId: author.id || m.userId,
    username: author.displayName || author.username || m.username || '未知用户',
    avatar: author.avatar || m.avatar,
    channelId: m.channelId || m.channel_id,
    // 附件 URL 拼接 API_BASE + access_token，供 <img>/<video> 浏览器原生标签使用
    attachments: normalizeAttachments(m.attachments),
  };
}

export const getMessages = (channelId: string, cursor?: string, limit = 50) =>
  apiGet<{ code: string; data: { items: any[]; hasMore: boolean; cursor?: string } }>(`/api/channels/${channelId}/messages?limit=${limit}${cursor ? `&cursor=${cursor}` : ''}`)
    .then(res => {
      if (res.data?.items) {
        res.data.items = res.data.items.map(normalizeMessage);
      }
      return res;
    });

// sendMessage 发送频道消息。服务端 CreateMessage 是 JSON 与 multipart 双绑定
//（handler.go：multipart 分支读 content/clientMessageId/parentId 表单字段 + attachments 文件字段）。
// T5/M9-①：files 形参此前被静默丢弃——现在真正实现：有文件走 multipart（含 parentId），
// 无文件保持 JSON body，行为向后兼容。
export const sendMessage = async (
  channelId: string,
  content: string,
  clientMessageId?: string,
  parentId?: string,
  files?: File[],
) => {
  if (files && files.length > 0) {
    const formData = new FormData();
    formData.append('content', content);
    if (clientMessageId) formData.append('clientMessageId', clientMessageId);
    if (parentId) formData.append('parentId', parentId);
    files.forEach(f => formData.append('attachments', f));
    const res = await upload<{ code: string; data: any }>(`/api/channels/${channelId}/messages`, formData);
    if (res.data) res.data = normalizeMessage(res.data);
    return res;
  }
  const res = await apiPost<{ code: string; data: any }>(`/api/channels/${channelId}/messages`, { content, clientMessageId, parentId });
  if (res.data) res.data = normalizeMessage(res.data);
  return res;
};

// sendMessageWithFiles sends a message with file attachments using multipart/form-data.
// T5/M9-①：补 parentId 尾参（服务端 multipart 分支支持）——线程回复带附件时
// 不再需要调用点自行组装 FormData。
export const sendMessageWithFiles = (
  channelId: string,
  content: string,
  files: File[],
  clientMessageId?: string,
  onProgress?: (loaded: number, total: number) => void,
  signal?: AbortSignal,
  parentId?: string,
) => {
  const formData = new FormData();
  formData.append('content', content);
  if (clientMessageId) formData.append('clientMessageId', clientMessageId);
  if (parentId) formData.append('parentId', parentId);
  files.forEach(f => formData.append('attachments', f));
  return upload<{ code: string; data: any }>(`/api/channels/${channelId}/messages`, formData, onProgress, signal)
    .then(res => {
      if (res.data) res.data = normalizeMessage(res.data);
      return res;
    });
};

// downloadChannelFile downloads a message attachment file.
export const downloadChannelFile = async (attachmentId: string, filename?: string) => {
  const blob = await downloadBlob(`/api/channel-files/${attachmentId}`);
  const url = window.URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename || 'download';
  document.body.appendChild(a);
  a.click();
  a.remove();
  window.URL.revokeObjectURL(url);
};
// F29: 未读信息（GET /api/channels/:id/unread）。
// 服务端 handler.GetUnreadCount → message.UnreadInfo，字段为 firstUnreadMessageId（注意不是 firstUnreadId）。
export const getUnreadInfo = (channelId: string) =>
  apiGet<{ code: string; data: { unreadCount: number; mentionCount: number; firstUnreadMessageId?: string } }>(`/api/channels/${channelId}/unread`);

// ===== 频道静音（T4/S-1）=====
// 用户对某频道的静音记录（服务端 model.UserChannelMute）。
// mutedUntil 为 null 表示永久静音；非 null 且已过期视为未静音（服务端清单接口已过滤过期行）。
export interface ChannelMute {
  id: string;
  userId: string;
  channelId: string;
  mutedUntil: string | null;
  createdAt: string;
}
// GET /api/channels/mutes —— 当前用户生效中的静音清单（供客户端启动时批量拉取）。
export const getChannelMutes = () =>
  apiGet<{ code: string; data: ChannelMute[] }>('/api/channels/mutes');
// PUT /api/channels/:id/mute —— 开/关本人对频道的静音；durationMinutes > 0 为限时静音，缺省永久。
export const setChannelMute = (channelId: string, muted: boolean, durationMinutes?: number) =>
  apiPut<{ code: string; data: { message: string } }>(`/api/channels/${channelId}/mute`, { muted, durationMinutes });

// T5/M9-②：服务端 GetThread 返回的键是 data.messages（handler.go），不是 items。
// 此前只归一化 data.items 导致 normalizeMessage 从不执行。现在以服务端实际键为准
//（保留 items 兼容防御），归一化结果同时写回两个键，消费方无论读哪个都拿到归一化后的数据。
export const getThread = (messageId: string, limit = 50) =>
  apiGet<{ code: string; data: { messages?: any[]; items?: any[]; hasMore: boolean; total?: number } }>(`/api/messages/${messageId}/thread?limit=${limit}`)
    .then(res => {
      if (res.data) {
        const raw = res.data.messages ?? res.data.items ?? [];
        const normalized = raw.map(normalizeMessage);
        res.data.messages = normalized;
        res.data.items = normalized;
      }
      return res;
    });

// T5/M16：标记频道已读（POST /api/channels/:id/mark-read，服务端另有旧路由 /read 同 handler）。
// lastMessageId 缺省时服务端自动取频道最新一条（handler.MarkChannelRead）。
// 读到中间某条时传该条 ID：服务端按「该条之后的消息」继续计未读（定位点语义）。
// 成功后服务端向本人广播 unread_count_changed（unreadCount/mentionCount=0 + firstUnreadMessageId=""）。
export const markChannelRead = (channelId: string, lastMessageId?: string) =>
  apiPost<{ code: string; data: { message: string } }>(`/api/channels/${channelId}/mark-read`, lastMessageId ? { lastMessageId } : {});

export const ackMessage = (messageId: string) =>
  apiPost<{ code: string; data: any }>(`/api/messages/${messageId}/ack`, {});
export const pinMessage = (channelId: string, messageId: string) =>
  apiPut<{ code: string; data: any }>(`/api/channels/${channelId}/pin`, { messageId });
export const unpinMessage = (channelId: string) =>
  apiDelete<{ code: string; data: any }>(`/api/channels/${channelId}/pin`);
export const getOGPreview = (url: string) =>
  apiGet<{ code: string; data: any }>(`/api/v1/preview?url=${encodeURIComponent(url)}`);
export const addReaction = (messageId: string, emoji: string) =>
  apiPost(`/api/messages/${messageId}/reactions`, { emoji });
export const removeReaction = (messageId: string, emoji: string) =>
  apiDelete(`/api/messages/${messageId}/reactions`, { emoji });

// Voice
export const getVoiceStatus = () => apiGet<{ code: string; data: any }>('/api/voice/status');
export const getVoiceToken = (channelId: string, identity?: string) =>
  apiPost<{ code: string; data: { token: string; livekitUrl: string; room: string; expiresIn: number } }>('/api/voice/token', { roomId: channelId, ...(identity ? { identity } : {}) });
export const refreshVoiceToken = (channelId: string, identity?: string) =>
  apiPost<{ code: string; data: { token: string; livekitUrl: string; room: string; expiresIn: number } }>('/api/voice/token/refresh', { roomId: channelId, ...(identity ? { identity } : {}) });
export const getVoiceParticipants = (channelId: string) =>
  apiGet<{ code: string; data: any[] }>(`/api/voice/participants/${channelId}`);
// M-04: 踢出语音频道成员（管理员）
export const kickVoiceParticipant = (userId: string, channelId: string) =>
  apiPost<{ code: string; data: any }>(`/api/voice/participants/${userId}/kick`, { roomId: channelId });
export const joinVoiceChannel = (channelId: string) =>
  apiPost('/api/voice/join', { roomId: channelId });
export const leaveVoiceChannel = (channelId: string) =>
  apiPost('/api/voice/leave', { roomId: channelId });
// E2EE（端到端加密）：取本房间的加密密钥。
// 服务端返回的是 hex 字符串口令（passphrase），客户端交给
// ExternalE2EEKeyProvider.setKey 走 PBKDF2 派生（与 Go/Rust SDK 参数一致）。
// 详见 文档/设计/DES-2026-0912-06-E2EE实施方案.md
export const getE2EEKey = (channelId: string) =>
  apiGet<{ code: string; data: { key: string } }>(`/api/voice/e2ee/key?roomId=${encodeURIComponent(channelId)}`);

// Bots
export const getBotStatus = (channelId?: string) =>
  apiGet<{ code: string; data: any }>(`/api/v1/bots/status${channelId ? `?channelId=${channelId}` : ''}`);
export const getBotQueue = (channelId: string) =>
  apiGet<{ code: string; data: { items: any[] } }>(`/api/v1/bots/queue/${channelId}`);
export const addToBotQueue = (channelId: string, title: string, artist?: string, duration?: number, source?: string, trackId?: string, cover?: string, album?: string, position?: number) =>
  apiPost<{ code: string; data: any }>('/api/v1/bots/queue', { channelId, title, artist, duration, source, trackId, cover, album, position });
export const removeFromBotQueue = (id: string) =>
  apiDelete<{ code: string; data: any }>(`/api/v1/bots/queue/${id}`);
export const reorderBotQueue = (channelId: string, queueIds: string[]) =>
  apiPut('/api/v1/bots/queue/reorder', { channelId, queueIds });
export const botSkip = (channelId?: string) =>
  apiPost('/api/v1/bots/skip', channelId ? { channelId } : undefined);
export const botPrevious = (channelId?: string) =>
  apiPost('/api/v1/bots/previous', channelId ? { channelId } : undefined);
export const botPause = (channelId?: string) =>
  apiPost('/api/v1/bots/pause', channelId ? { channelId } : undefined);
export const botResume = (channelId?: string) =>
  apiPost('/api/v1/bots/resume', channelId ? { channelId } : undefined);
export const botSeek = (time: number, channelId?: string) =>
  apiPost('/api/v1/bots/seek', { time, channelId });
export const botPlayNow = (queueId: string, channelId?: string) =>
  apiPost('/api/v1/bots/playnow', { queueId, channelId });
export const botSetMode = (mode: string, channelId?: string) =>
  apiPost('/api/v1/bots/mode', { mode, channelId });
export const botSetVolume = (volume: number, channelId?: string) =>
  apiPost('/api/v1/bots/volume', { volume, channelId });
export const botQueueClear = (channelId?: string) =>
  apiPost('/api/v1/bots/queue/clear', channelId ? { channelId } : undefined);
export const botTTS = (text: string, channelId?: string, voice?: string, speed?: number, pitch?: number, volume?: number) =>
  apiPost('/api/v1/bots/tts', { text, channelId, voice, speed, pitch, volume });
export const recordTTSConsent = () => apiPost('/api/v1/bots/tts/consent', {});
// 查询当前用户的 TTS consent 状态（取代 localStorage 读取）
// 设计参考：Mattermost Preferences API（后端为权威）
// 响应：{ code, message, data: { consented: boolean } }
export const getTTSConsent = () => apiGet('/api/v1/bots/tts/consent');
// 撤回 TTS consent（与 recordTTSConsent 对称，写入 revoked_at 审计字段）
export const revokeTTSConsent = () => apiDelete('/api/v1/bots/tts/consent');
export const uploadAudio = (
  file: File,
  onProgress?: (loaded: number, total: number) => void,
  signal?: AbortSignal
) => {
  const formData = new FormData();
  formData.append('file', file);
  return upload<{ code: string; data: any }>('/api/v1/bots/upload', formData, onProgress, signal);
};
export const getBotUploads = () => apiGet<{ code: string; data: { items: any[] } }>('/api/v1/bots/uploads');
export const deleteUpload = (id: string) => apiDelete<{ code: string; data: any }>(`/api/v1/bots/uploads/${id}`);
export const enqueueUpload = (id: string, channelId: string) =>
  apiPost<{ code: string; data: any }>(`/api/v1/bots/uploads/${id}/enqueue`, { channelId });
export const priorityQueue = (id: string, channelId: string) =>
  apiPatch<{ code: string; data: any }>(`/api/v1/bots/queue/${id}/priority`, { channelId });

// Admin
export const getAdminBootstrapStatus = () =>
  apiGet<{ code: string; data: { initialized: boolean; needsBootstrap: boolean } }>('/api/admin/bootstrap/status');
export const adminBootstrapVerify = (token: string) =>
  apiPost<{ code: string; data: { valid: boolean } }>('/api/admin/bootstrap/verify', { token });
export const adminBootstrapRegister = (data: { bootstrapToken: string; username: string; displayName?: string; email: string; password: string; spaceName?: string; network?: any }) =>
  apiPost<{ code: string; data: any }>('/api/admin/bootstrap/register', data);
export const adminLogin = (email: string, password: string) =>
  apiPost<{ code: string; data: any }>('/api/admin/login', { email, password });
export const getAdminConfig = () => apiGet<{ code: string; data: any }>('/api/admin/config');
export const updateAdminConfig = (data: any) => apiPut('/api/admin/config', data);
export const getAdminNetwork = () => apiGet<{ code: string; data: { network: any; suggestedSrvRecords: string[]; srvTcpPredicate: string; srvUdpPredicate: string } }>('/api/admin/network');
export const updateAdminNetwork = (data: any) => apiPut<{ code: string; data: any }>('/api/admin/network', data);
export const detectAdminNetwork = () => apiGet<{ success: boolean; error?: string; result: any; recommendations: Array<{ method: string; priority: number; reason: string }> }>('/api/admin/network/detect');
// 后端 VerifyNetwork 直接返回顶层 {success, result}（非标准信封，见 admin/handler.go），
// 因此这里按扁平结构标注；调用方读 res.result，而不是 res.data.result。
export const verifyAdminNetwork = (network?: any) => apiPost<{ success: boolean; result: any }>('/api/admin/network/verify', network ? { network } : undefined);
export const getAdminUsers = (page = 1, pageSize = 20, keyword?: string) =>
  apiGet<{ code: string; data: any }>(`/api/admin/users?page=${page}&pageSize=${pageSize}${keyword ? `&keyword=${encodeURIComponent(keyword)}` : ''}`);
export const getAdminRuntime = () => apiGet<{ code: string; data: any }>('/api/admin/runtime');
export const getAdminModules = () => apiGet<{ code: string; data: any }>('/api/admin/modules');
export const getAdminStorage = () => apiGet<{ code: string; data: any }>('/api/admin/storage');
export const getAdminSystemUsage = () => apiGet<{ code: string; data: { total: number; used: number; free: number; percent: number } }>('/api/admin/system/usage');
export const deleteAdminUser = (userId: string) => apiDelete<{ code: string; data: any }>(`/api/admin/users/${userId}`);
export const updateUserRole = (userId: string, role: string) =>
  apiPut<{ code: string; data: any }>(`/api/admin/users/${userId}/role`, { role });
export const updateUserStatus = (userId: string, isActive: boolean) =>
  apiPatch<{ code: string; data: any }>(`/api/admin/users/${userId}/status`, { isActive });
export const resetUserPassword = (userId: string, newPassword: string) =>
  apiPost<{ code: string; data: any }>(`/api/admin/users/${userId}/reset-password`, { new_password: newPassword });
export const transferChannelOwnership = (channelId: string, newOwnerId: string) =>
  apiPost<{ code: string; data: any }>(`/api/admin/channels/${channelId}/transfer`, { new_owner_id: newOwnerId });
// P1-1: 管理后台的频道管理必须走 admin 专用接口（C-3）。
// 此前管理页误用成员级 /api/channels：那是给普通用户端的接口，权限模型不同，
// 且用户端接口一旦变更就会连带弄坏管理页。admin 接口的路径参数名是 channelId。
export const getAdminChannels = () =>
  apiGet<{ code: string; data: any[] }>('/api/admin/channels');
export const createAdminChannel = (data: any) =>
  apiPost<{ code: string; data: any }>('/api/admin/channels', data);
export const updateAdminChannel = (channelId: string, data: any) =>
  apiPatch<{ code: string; data: any }>(`/api/admin/channels/${channelId}`, data);
export const deleteAdminChannel = (channelId: string) =>
  apiDelete<{ code: string; data: any }>(`/api/admin/channels/${channelId}`);
// H2: channel permission management API
export const getChannelPermissions = (channelId: string) =>
  apiGet<{ code: string; data: { read_roles: string[]; write_roles: string[]; visible_roles: string[]; is_public: boolean } }>(`/api/admin/channels/${channelId}/permissions`);
export const updateChannelPermissions = (channelId: string, data: { read_roles: string[]; write_roles: string[]; visible_roles: string[]; is_public: boolean }) =>
  apiPut<{ code: string; data: any }>(`/api/admin/channels/${channelId}/permissions`, data);
export const toggleModule = (moduleName: string, action: 'enable' | 'disable') =>
  apiPost<{ code: string; data: any }>(`/api/admin/modules/${moduleName}/actions`, { action });
export const getAdminLogs = (page = 1, pageSize = 20) =>
  apiGet<{ code: string; data: any }>(`/api/admin/audit-logs?page=${page}&pageSize=${pageSize}`);
// A11 (DES-20261001-01 §12.3): 管理后台监控告警列表与静默。
// 列表默认返回活跃告警 + 最近 50 条已恢复历史；includeResolved=false 只返回活跃。
export const getAdminAlerts = (includeResolved = true, limit = 50) =>
  apiGet<{ code: string; data: { active: any[]; resolved: any[]; activeCount: number } }>(
    `/api/admin/alerts?includeResolved=${includeResolved}&limit=${limit}`,
  );
// 静默按 type 生效：期间评估器照常写表，但同 type 不再向 admin WS 推送。缺省 24h。
export const muteAdminAlert = (id: string, hours = 24) =>
  apiPost<{ code: string; data: { alert: any; action: string } }>(
    `/api/admin/alerts/${id}/mute`, { hours },
  );
// Task 7: 端口配置面板 —— 获取端口列表与服务重启
// api() 返回完整信封 {code, message, data, meta}，端口列表在 data.ports 下。
export const getAdminPorts = () =>
  apiGet<{ code: string; data: { ports: PortInfo[] } }>('/api/admin/ports').then(res => res.data.ports);
// P1-6: 信封顶层 message 恒为 "success"（core/errors.Success），真正的提示文案
// 在 data.message 下。这里直接返回可展示的中文文案，调用方不再有机会读到 "success"。
export const restartAdminService = (service: string): Promise<string> =>
  apiPost<{ code: string; message: string; data: { success: boolean; message: string } }>(
    '/api/admin/service/restart', { service },
  ).then(res => res.data?.message || '重启指令已发送，服务将在 3-5 秒后重新连接');

// Whiteboard
export const listWhiteboards = () =>
  apiGet<{ code: string; data: Whiteboard[] }>('/api/whiteboards').then(res => {
    if (Array.isArray(res.data)) res.data = res.data.map(normalizeWhiteboard)
    return res
  });
export const createWhiteboard = (data: { name: string }) =>
  apiPost<{ code: string; data: Whiteboard }>('/api/whiteboards', data).then(res => {
    if (res.data) res.data = normalizeWhiteboard(res.data)
    return res
  });
export const updateWhiteboard = (id: string, data: { name?: string; archived?: boolean }) =>
  apiPatch<{ code: string; data: Whiteboard }>(`/api/whiteboards/${id}`, data).then(res => {
    if (res.data) res.data = normalizeWhiteboard(res.data)
    return res
  });
export const deleteWhiteboard = (id: string) =>
  apiDelete<{ code: string; data: any }>(`/api/whiteboards/${id}`);
export const getWhiteboardStrokes = (whiteboardId: string) =>
  apiGet<{ code: string; data: WhiteboardStroke[] }>(`/api/whiteboards/${whiteboardId}/strokes`);
export const createWhiteboardStroke = (whiteboardId: string, data: { tool: string; data: string; color: string; width: number }) =>
  apiPost<{ code: string; data: WhiteboardStroke }>(`/api/whiteboards/${whiteboardId}/strokes`, data);
export const clearWhiteboardStrokes = (whiteboardId: string) =>
  apiDelete<{ code: string; data: any }>(`/api/whiteboards/${whiteboardId}/strokes`);
export const deleteWhiteboardStroke = (whiteboardId: string, strokeId: string) =>
  apiDelete<{ code: string; data: any }>(`/api/whiteboards/${whiteboardId}/strokes/${strokeId}`);
export const uploadWhiteboardThumbnail = (whiteboardId: string, base64Png: string) =>
  apiPost<{ code: string; data: { url: string } }>(`/api/whiteboards/${whiteboardId}/thumbnail`, { image: base64Png }).then(res => {
    if (res.data?.url) {
      const resolved = resolveServerAssetUrl(res.data.url)
      if (resolved) res.data.url = withCacheVersion(resolved)
    }
    return res
  });

// Sharedoc
export const getSharedocs = () => apiGet<{ code: string; data: any[] }>('/api/sharedoc/list');
export const createSharedoc = (data: { title: string; content?: string }) =>
  apiPost<{ code: string; data: any }>('/api/sharedoc/create', data);
export const updateSharedoc = (id: string, data: { title?: string; content?: string; expectedVersion?: number }) =>
  apiPatch<{ code: string; data: any }>(`/api/sharedoc/${id}`, data);
export const deleteSharedoc = (id: string) =>
  apiDelete<{ code: string; data: any }>(`/api/sharedoc/${id}`);
export const getSharedocEditors = (id: string) =>
  apiGet<{ code: string; data: { docId: string; editors: any[]; count: number } }>(`/api/sharedoc/${id}/editors`);
export const getSharedocVersions = (id: string) =>
  apiGet<{ code: string; data: any[] }>(`/api/sharedoc/${id}/versions`);
export const getSharedocVersion = (id: string, versionNo: number) =>
  apiGet<{ code: string; data: any }>(`/api/sharedoc/${id}/versions/${versionNo}`);
export const restoreSharedocVersion = (id: string, versionNo: number) =>
  apiPost<{ code: string; data: any }>(`/api/sharedoc/${id}/versions/${versionNo}/restore`, {});
export const getSharedocVersionsCompare = (id: string, a: number, b: number) =>
  apiGet<{ code: string; data: { a: number; b: number; diff: any[] } }>(`/api/sharedoc/${id}/versions/compare?a=${a}&b=${b}`);
export const getSharedocComments = (id: string) =>
  apiGet<{ code: string; data: any[] }>(`/api/sharedoc/${id}/comments`);
export const createSharedocComment = (id: string, data: { parentId?: string; anchorText?: string; anchorOffset?: number; anchorLength?: number; content: string }) =>
  apiPost<{ code: string; data: any }>(`/api/sharedoc/${id}/comments`, data);
export const updateSharedocComment = (id: string, commentId: string, content: string) =>
  apiPatch<{ code: string; data: any }>(`/api/sharedoc/${id}/comments/${commentId}`, { content });
export const deleteSharedocComment = (id: string, commentId: string) =>
  apiDelete<{ code: string; data: any }>(`/api/sharedoc/${id}/comments/${commentId}`);
export const resolveSharedocComment = (id: string, commentId: string) =>
  apiPost<{ code: string; data: any }>(`/api/sharedoc/${id}/comments/${commentId}/resolve`, {});
export const reopenSharedocComment = (id: string, commentId: string) =>
  apiPost<{ code: string; data: any }>(`/api/sharedoc/${id}/comments/${commentId}/reopen`, {});

// Schedule
export const getScheduleEvents = () => apiGet<{ code: string; data: any[] }>('/api/schedule/events');
// DES-20261001-01 §9.3：body 增可选 inviteeIds（≤50、去重、空间成员、不含创建者，由服务端校验）。
// 不传或空数组 = 普通事件，行为与旧版完全一致。
export const createScheduleEvent = (data: { title: string; description?: string; startTime?: string; endTime?: string; allDay?: boolean; location?: string; reminderMinutes?: number; inviteeIds?: string[] }) =>
  apiPost<{ code: string; data: any }>('/api/schedule/events', data);
export const updateScheduleEvent = (id: string, data: { title?: string; description?: string; startTime?: string; endTime?: string; allDay?: boolean; location?: string; reminderMinutes?: number }) =>
  apiPut<{ code: string; data: any }>(`/api/schedule/events/${id}`, data);
export const deleteScheduleEvent = (id: string) =>
  apiDelete<{ code: string; data: any }>(`/api/schedule/events/${id}`);

// ===== 日程事件邀请 RSVP（DES-20261001-01 §9.2-9.3）=====
// 单个受邀人的邀请状态。status 三态：pending（待应答）/ accepted（接受）/ declined（拒绝）。
export interface ScheduleEventInvite {
  userId: string;
  displayName: string;
  status: 'pending' | 'accepted' | 'declined';
  respondedAt: string | null;
}

// GET /schedule/events/:id/invites —— 返回 { invites: [...] }；可见者 = 创建者 + 受邀人。
// 非创建者且非受邀人时服务端返回 404，调用方应按「无数据」静默处理（不应打断 UI）。
export const getEventInvites = (eventId: string) =>
  apiGet<{ code: string; data: { invites: ScheduleEventInvite[] } }>(`/api/schedule/events/${encodeURIComponent(eventId)}/invites`);

// PUT /schedule/events/:id/invite —— 受邀人本人应答；body {status: "accepted"|"declined"}；服务端幂等。
export const respondEventInvite = (eventId: string, status: 'accepted' | 'declined') =>
  apiPut<{ code: string; data: { message?: string } }>(`/api/schedule/events/${encodeURIComponent(eventId)}/invite`, { status });

// CloudFS
export const getCloudFSList = (path = '/') =>
  apiGet<{ code: string; data: { path: string; items: any[] } }>(`/api/cloudfs/list?path=${encodeURIComponent(path)}`);
// M-02: 获取云文件存储配额
export const getCloudFSUsage = () =>
  apiGet<{ code: string; data: { used: number; quota: number; usedPercent: number } }>('/api/cloudfs/usage');
export const createCloudFSFolder = (path: string, name: string) =>
  apiPost<{ code: string; data: any }>('/api/cloudfs/folder', { path, name });
export const uploadCloudFS = (path: string, file: File | Blob, onProgress?: (loaded: number, total: number) => void) => {
  const formData = new FormData();
  formData.append('path', path);
  formData.append('file', file);
  return upload<{ code: string; data: any }>('/api/cloudfs/upload', formData, onProgress);
};
export const deleteCloudFS = (path: string) =>
  apiDelete<{ code: string; data: any }>(`/api/cloudfs/delete?path=${encodeURIComponent(path)}`);

// DES-2026-0912-02：内联预览 / 重命名 / 移动（服务端第一批；均为 path 语义，与既有 CloudFS API 一致）。
// 预览：返回可直接用于 <img>/<video>/<iframe> 的 URL（需鉴权头，故前端应走 fetch+Blob 或带 token 的请求）；
// 白名单外（含 SVG/HTML）服务端回落 `Content-Disposition: attachment`，此 URL 仍可安全用于下载。
export const previewCloudFSURL = (path: string) =>
  `/api/cloudfs/preview?path=${encodeURIComponent(path)}`;
export const previewCloudFSWithChecksum = (path: string): Promise<DownloadResult> =>
  downloadBlobWithChecksum(previewCloudFSURL(path));
export const previewCloudFS = async (path: string) =>
  (await previewCloudFSWithChecksum(path)).blob;
export const renameCloudFS = (path: string, newName: string) =>
  apiPatch<{ code: string; data: any }>('/api/cloudfs/rename', { path, newName });
export const moveCloudFS = (path: string, targetPath: string) =>
  apiPost<{ code: string; data: any }>('/api/cloudfs/move', { path, targetPath });

// 云文件第二批（DES-2026-0912-02 §4.3）回收站。
// 服务端只按「顶层条目」寻址：父目录本身也在回收站里的子项不会单独列出，
// 也不能被单独还原/清除（与桌面回收站一致）。UI 必须遵循这个语义。
export interface CloudFSTrashItem {
  id: string;
  name: string;
  type: 'folder' | 'file';
  path: string;
  size?: string;
  uploadBy?: string;
  /** "YYYY-MM-DD HH:mm"（服务端已格式化，非 ISO） */
  deletedAt: string;
  /** 同上格式，超过保留期后由后台任务彻底清除；服务端未下发时省略 */
  expiresAt?: string;
}

export const getCloudFSTrash = () =>
  apiGet<{ code: string; data: { items: CloudFSTrashItem[]; retainDays: number } }>('/api/cloudfs/trash');

// 还原/彻底删除都按 id 优先、path 兜底寻址（与服务端 resolveTrashItem 一致）
const trashLocator = (item: { id?: string; path?: string }) =>
  item.id ? `id=${encodeURIComponent(item.id)}` : `path=${encodeURIComponent(item.path || '')}`;

export const restoreCloudFSTrash = (item: { id?: string; path?: string }) =>
  apiPost<{ code: string; data: { message: string; type: string; path: string; name: string } }>(
    '/api/cloudfs/trash/restore',
    item.id ? { id: item.id } : { path: item.path },
  );

export const purgeCloudFSTrash = (item: { id?: string; path?: string }) =>
  apiDelete<{ code: string; data: any }>(`/api/cloudfs/trash?${trashLocator(item)}`);

export const emptyCloudFSTrash = () =>
  apiDelete<{ code: string; data: { message: string; purged: number } }>('/api/cloudfs/trash/all');

// 云文件第二批（DES-2026-0912-02 §4.4）搜索。
// 按文件名模糊匹配，只返回文件、不含文件夹；服务端上限 100 条。
// 不传 path → 搜索整个 Space；传了 → 只搜该目录子树。
export interface CloudFSSearchItem {
  id: string;
  name: string;
  type: 'file';
  /** 命中项的完整路径；跨目录命中时只有它能定位到文件 */
  path?: string;
  size?: string;
  uploadBy?: string;
  updatedAt: string;
}

export const searchCloudFS = (query: string, path?: string) => {
  const params = new URLSearchParams();
  params.set('q', query);
  if (path) params.set('path', path);
  return apiGet<{ code: string; data: { items: CloudFSSearchItem[]; query: string } }>(
    `/api/cloudfs/search?${params.toString()}`,
  );
};

// 云文件分享链接（DES-2026-0912-05 §9，客户端接线）。
// 管理三条（创建/列表/撤销）挂既有 cloudfs 鉴权组，路径与其它 CloudFS API 同前缀；
// 公开三条（元信息/verify/download）只在 /api/v1/share 下，**没有** /api legacy 别名，
// 因此分享链接的 URL 必须以服务端拼好的 sharePath（"/api/v1/share/<token>"）为准。
// 明文 token 只在创建响应里出现一次（服务端只存 sha256），列表接口永远不会返回链接。

export interface CloudFileShareCreateOptions {
  /** cloudfs 文件 ID（SharedFileEntry.ID），不接受路径/文件名 */
  fileId: string;
  /** 有效期天数：缺省或 0 = 服务端默认 7 天；允许 1..30 */
  expiresInDays?: number;
  /** 可选密码（≤128 字符）；空/缺省 = 无密码 */
  password?: string;
  /** 下载次数上限：0 或缺省 = 不限 */
  maxDownloads?: number;
}

export interface CloudFileShareCreated {
  id: string;
  fileId: string;
  /** 明文分享令牌：仅此一次，之后无法再取回 */
  token: string;
  /** 前 8 位，仅供列表人工辨识，不参与校验 */
  tokenPrefix: string;
  /** "/api/v1/share/<token>"，服务端拼好的相对路径 */
  sharePath: string;
  /** 服务端配置了 PublicAddress 时下发 = PublicAddress + sharePath；否则缺省 */
  url?: string;
  expiresAt: string;
  maxDownloads: number;
  requiresPassword: boolean;
}

export interface CloudFileShareItem {
  id: string;
  fileId: string;
  /** 分享指向的文件名；文件已删除（含回收站）时缺省 */
  fileName?: string;
  fileDeleted?: boolean;
  tokenPrefix: string;
  requiresPassword: boolean;
  expiresAt: string;
  maxDownloads: number;
  downloadCount: number;
  createdAt: string;
  revoked: boolean;
  expired: boolean;
  revokedAt?: string;
  lastAccessAt?: string;
}

export const createCloudFileShare = (opts: CloudFileShareCreateOptions) =>
  apiPost<{ code: string; data: CloudFileShareCreated }>('/api/cloudfs/share', {
    fileId: opts.fileId,
    expiresInDays: opts.expiresInDays ?? 0,
    password: opts.password ?? '',
    maxDownloads: opts.maxDownloads ?? 0,
  });

export const getCloudFileShares = (fileId?: string) =>
  apiGet<{ code: string; data: { items: CloudFileShareItem[] } }>(
    `/api/cloudfs/shares${fileId ? `?fileId=${encodeURIComponent(fileId)}` : ''}`,
  );

export const revokeCloudFileShare = (shareId: string) =>
  apiDelete<{ code: string; data: { id: string; revoked: boolean; revokedAt?: string } }>(
    `/api/cloudfs/share/${encodeURIComponent(shareId)}`,
  );

/**
 * 拼接用户可分发的分享链接。
 * 优先用服务端下发的 url（PublicAddress 口径，与访问者实际使用的地址可能不同，
 * 但它是部署方声明的对外地址）；否则用当前 API base 的 origin 拼 sharePath；
 * 两者皆缺（无 api base 的极端场景）退化为相对 sharePath。
 */
export function buildShareLink(
  created: Pick<CloudFileShareCreated, 'url' | 'sharePath'>,
  apiBase: string,
): string {
  if (created.url) return created.url;
  if (apiBase) return `${apiBase}${created.sharePath}`;
  return created.sharePath;
}

/**
 * 创建分享表单的本地校验（发请求前拦截，与服务端 ShareOptions 约束一致）。
 * 返回错误文案数组；为空数组即通过。密码/次数/有效期的文案与服务端
 * chinese_message 口径对齐，避免「本地放过、服务端打回」的体验分裂。
 */
export function validateShareForm(opts: CloudFileShareCreateOptions): string[] {
  const errors: string[] = [];
  if (!opts.fileId || !opts.fileId.trim()) errors.push('缺少文件 ID，无法创建分享');
  const days = opts.expiresInDays ?? 0;
  // 0 = 交给服务端取默认 7 天（与 ShareOptions.ExpiresInDays 语义一致），合法
  if (!Number.isInteger(days) || days < 0 || days > 30) {
    errors.push('有效期必须在 1 到 30 天之间');
  }
  if (opts.maxDownloads !== undefined && (!Number.isInteger(opts.maxDownloads) || opts.maxDownloads < 0)) {
    errors.push('下载次数上限不能为负数');
  }
  if (opts.password && opts.password.length > 128) {
    errors.push('分享密码不能超过 128 个字符');
  }
  return errors;
}

/** 分享条目的展示状态（已撤销 > 已过期 > 次数用尽 > 文件已删除 > 有效中） */
export function describeShareStatus(item: CloudFileShareItem): string {
  if (item.revoked) return '已撤销';
  if (item.expired) return '已过期';
  if (item.maxDownloads > 0 && item.downloadCount >= item.maxDownloads) return '次数用尽';
  if (item.fileDeleted) return '文件已删除';
  return '有效中';
}

/** 下载计数展示："3 / 10"；maxDownloads=0 表示不限 */
export function formatShareDownloads(item: Pick<CloudFileShareItem, 'downloadCount' | 'maxDownloads'>): string {
  return `${item.downloadCount} / ${item.maxDownloads > 0 ? item.maxDownloads : '不限'}`;
}

/**
 * 分享操作的错误兜底映射。服务端已通过 chinese_message 下发中文文案
 * （api() 已放入 err.message），因此本函数只处理技术性兜底：
 * err.message 缺失或为 "HTTP xxx"/"Network error" 这类非业务文案时，
 * 按 err.code 给中文，再兜不住则用调用方给的 fallback。
 */
export function mapShareError(err: any, fallback: string): string {
  const msg: string = err?.message || '';
  const isTechnical = !msg || /^(HTTP \d+|Network error|Request timeout|Failed to fetch)$/i.test(msg);
  if (!isTechnical) return msg;
  const code: string = err?.code || '';
  const byCode: Record<string, string> = {
    SHARE_TTL_INVALID: '有效期必须在 1 到 30 天之间',
    SHARE_MAX_DOWNLOADS_INVALID: '下载次数上限不能为负数',
    SHARE_PASSWORD_INVALID: '分享密码不合法（不能超过 128 个字符）',
    SHARE_NOT_FOUND: '分享链接不存在或已失效',
    CLOUDFS_PERMISSION_DENIED: '没有操作权限：仅创建者本人或空间管理员可以撤销分享',
    CLOUDFS_FILE_NOT_FOUND: '文件不存在或已被删除',
    SYSTEM_RATE_LIMITED: '操作过于频繁，请稍后再试',
  };
  return byCode[code] || fallback;
}

// D28: 下载二进制文件辅助函数，返回 Blob，支持 401 自动刷新 token。
// Win 端用于同步到磁盘等场景；Web 端 downloadCloudFS 复用此实现。
export async function downloadBlob(path: string): Promise<Blob> {
  return (await downloadBlobWithChecksum(path)).blob;
}

// 云文件第二批（DES-2026-0912-02 §4.6）完整性校验。
// 服务端 download/preview 在「有值时」返回 X-File-Checksum: sha256=<hex>；
// 早于该校验能力上传的历史文件不带该头，因此**必须按可选处理**。
export const CLOUDFS_CHECKSUM_HEADER = 'X-File-Checksum';

export interface DownloadResult {
  blob: Blob;
  /** 十六进制小写 sha256；服务端未下发该头时为 null */
  checksum: string | null;
}

/** 解析 `sha256=<hex>` 响应头；缺失或格式不符一律返回 null（按可选处理） */
export function parseChecksumHeader(raw: string | null | undefined): string | null {
  if (!raw) return null;
  const m = /^\s*sha256\s*=\s*([0-9a-fA-F]{64})\s*$/.exec(raw);
  return m ? m[1].toLowerCase() : null;
}

/** 对内存中的字节重算 sha256（十六进制小写） */
export async function sha256Hex(data: ArrayBuffer): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', data);
  return Array.from(new Uint8Array(digest))
    .map(b => b.toString(16).padStart(2, '0'))
    .join('');
}

/**
 * 校验下载到的 Blob 与响应头声明的摘要是否一致。
 * - checksum 为 null（历史文件未带该头）→ 返回 { verified: false }，**不报错**；
 * - 不一致 → 抛错，由调用方决定如何提示（内容已损坏，不能静默放行）。
 */
export async function verifyBlobChecksum(
  blob: Blob,
  checksum: string | null,
): Promise<{ verified: boolean; actual: string | null }> {
  if (!checksum) return { verified: false, actual: null };
  const actual = await sha256Hex(await blob.arrayBuffer());
  if (actual !== checksum) {
    throw new Error(`文件完整性校验失败：下载内容与服务器记录不一致（期望 sha256=${checksum}，实际 ${actual}）`);
  }
  return { verified: true, actual };
}

/** 下载并回传响应头声明的校验值（未下发时为 null），供调用方自行比对 */
export async function downloadBlobWithChecksum(path: string): Promise<DownloadResult> {
  const doDownload = async (tokenOverride?: string): Promise<DownloadResult> => {
    const token = tokenOverride || getAccessToken() || '';
    const headers: Record<string, string> = {};
    if (token) headers['Authorization'] = `Bearer ${token}`;
    if (CLIENT_TYPE) headers['X-Client-Type'] = CLIENT_TYPE;
    const res = await fetch(`${API_BASE_STATE.value}${path}`, {
      method: 'GET',
      credentials: 'include',
      headers,
    });
    if (res.status === 401) {
      let code: string | undefined;
      try { const data = await res.clone().json(); code = data?.code; } catch {}
      if (code === 'AUTH_TOKEN_EXPIRED') {
        try {
          const newToken = await refreshAccessToken();
          return doDownload(newToken);
        } catch {
          clearStoredAuthTokens();
          throw new Error('登录已过期，请重新登录');
        }
      }
      if (code === 'AUTH_TOKEN_MISSING' || code === 'AUTH_TOKEN_INVALID' || code === 'AUTH_UNAUTHORIZED') {
        clearStoredAuthTokens();
      }
      throw new Error('下载失败: 未授权');
    }
    if (!res.ok) throw new Error(`下载失败: HTTP ${res.status}`);
    return {
      blob: await res.blob(),
      checksum: parseChecksumHeader(res.headers.get(CLOUDFS_CHECKSUM_HEADER)),
    };
  };
  return doDownload();
}

// 云文件下载：取回内容后按响应头声明的 sha256 校验（未带该头的历史文件跳过），
// 校验不通过直接抛错 —— 不把不一致的内容交给用户。
export const downloadCloudFSWithChecksum = (path: string): Promise<DownloadResult> =>
  downloadBlobWithChecksum(`/api/cloudfs/download?path=${encodeURIComponent(path)}`);

export const downloadCloudFS = async (path: string, filename?: string) => {
  const { blob, checksum } = await downloadCloudFSWithChecksum(path);
  await verifyBlobChecksum(blob, checksum);
  const url = window.URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename || path.split('/').pop() || 'download';
  document.body.appendChild(a);
  a.click();
  a.remove();
  window.URL.revokeObjectURL(url);
};

// C-6: 导出审计日志 CSV。后端 GET /api/admin/audit-logs/export 返回文件流
// （Content-Disposition: attachment），需鉴权头，故复用 downloadBlob 取 Blob 后
// 触发浏览器下载，而不是 apiGet（后者按 JSON 解析）。
export const exportAdminAuditLogs = async (filename = 'audit-logs.csv') => {
  const blob = await downloadBlob('/api/admin/audit-logs/export');
  const url = window.URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  window.URL.revokeObjectURL(url);
};

// Screenshare
// D29: 屏幕共享参数类型（Win 端定义），opts 可选以兼容 Web 端仅传 channelId 的调用
export interface StartScreenshareOptions {
  shareType: 'screen' | 'window' | 'application';
  sourceId: string;
  resolution: string;
  frameRate: number;
  maxBitrate: number;
  shareAudio: boolean;
  suppressVoice: boolean;
}

export const startScreenshare = (channelId: string, opts?: StartScreenshareOptions) =>
  apiPost<{ code: string; data: any }>('/api/screenshare/start', opts ? { channelId, ...opts } : { channelId });
export const stopScreenshare = (channelId: string) =>
  apiPost<{ code: string; data: any }>('/api/screenshare/stop', { channelId });
export const getScreenshareStatus = (channelId: string) =>
  apiGet<{ code: string; data: any }>(`/api/screenshare/status?channelId=${encodeURIComponent(channelId)}`);

// Minigames
export interface MinigameInfo {
  id: string;
  name: string;
  players: number;
}

export interface MinigamePlayer {
  userId: string;
  username: string;
  joinedAt?: number;
}

export interface MinigameSession {
  sessionId: string;
  gameType: string;
  spaceId: string;
  bindChannelId?: string;
  hostId: string;
  status?: 'waiting' | 'playing' | 'paused' | 'ended';
  isActive: boolean;
  players?: MinigamePlayer[];
  spectators?: MinigamePlayer[];
}

export interface MinigameRoomItem {
  sessionId: string;
  gameType: string;
  gameName: string;
  hostId: string;
  status: string;
  players: MinigamePlayer[];
  spectators: MinigamePlayer[];
  playerCount: number;
  spectatorCount: number;
  bindChannelId?: string;
}

export const getMinigames = () =>
  apiGet<{ code: string; data: MinigameInfo[] }>('/api/v1/minigames/list');

export const joinMinigameRoom = (
  gameType: string,
  opts?: { channelId?: string; sessionId?: string; ruleMode?: 'classic' | 'laizi' }
) =>
  apiPost<{ code: string; data: MinigameSession }>('/api/v1/minigames/join', {
    gameType,
    ...opts,
  });

export const leaveMinigameRoom = (sessionId: string) =>
  apiPost<{ code: string; data: { message: string } }>('/api/v1/minigames/leave', { sessionId });

export const minigameAction = (sessionId: string, action: string, payload?: unknown) =>
  apiPost<{ code: string; data: { message: string; sessionId: string; status?: string } }>(
    '/api/v1/minigames/action',
    { sessionId, action, payload }
  );

// D30: 向后兼容别名（Win 端旧调用点 joinMinigame/leaveMinigame/actionMinigame 仍可用）
export const joinMinigame = (channelId: string, gameType: string) =>
  joinMinigameRoom(gameType, { channelId });
export const leaveMinigame = leaveMinigameRoom;
export const actionMinigame = minigameAction;

export const getMinigameState = (sessionId: string) =>
  apiGet<{ code: string; data: any }>(`/api/v1/minigames/sessions/${sessionId}/state`);

export const getMinigameRooms = (spaceId?: string, gameType?: string) => {
  const params = new URLSearchParams();
  if (spaceId) params.set('spaceId', spaceId);
  if (gameType) params.set('gameType', gameType);
  const qs = params.toString();
  return apiGet<{ code: string; data: { spaceId: string; games: MinigameRoomItem[] } }>(
    `/api/v1/minigames/rooms${qs ? `?${qs}` : ''}`
  );
};

// Backward compatible: returns active games in the channel's space.
export const getMinigameChannelStatus = (channelId: string) =>
  apiGet<{ code: string; data: any }>(`/api/v1/minigames/channel-status?channelId=${encodeURIComponent(channelId)}`);

export const inviteToMinigame = (sessionId: string, toUserId?: string) =>
  apiPost<{ code: string; data: { message: string } }>('/api/v1/minigames/invite', { sessionId, toUserId });

export const minigameChat = (sessionId: string, content: string) =>
  apiPost<{ code: string; data: { messageId?: string } }>('/api/v1/minigames/chat', { sessionId, content });

export interface LeaderboardEntry {
  rank: number;
  userId: string;
  username: string;
  bestScore: number;
  updatedAt: string;
}

export const submitScore = (gameType: string, score: number) =>
  apiPost<{ code: string; data: { bestScore: number; updated: boolean } }>('/api/v1/minigames/leaderboard', {
    gameType,
    score,
  });

export const getLeaderboard = (gameType: string, limit = 20) =>
  apiGet<{ code: string; data: { gameType: string; entries: LeaderboardEntry[] } }>(
    `/api/v1/minigames/leaderboard?gameType=${encodeURIComponent(gameType)}&limit=${limit}`
  );

export const getMyScore = (gameType: string) =>
  apiGet<{ code: string; data: { bestScore: number; updatedAt?: string } }>(
    `/api/v1/minigames/scores/me?gameType=${encodeURIComponent(gameType)}`
  );

// VirtualNet
export const getVirtualNetStatus = () => apiGet<{ code: string; data: any }>('/api/virtualnet/status');
export const connectVirtualNet = (networkCidr?: string) =>
  apiPost<{ code: string; data: any }>('/api/virtualnet/connect', networkCidr ? { networkCidr } : {});
export const disconnectVirtualNet = () =>
  apiPost<{ code: string; data: any }>('/api/virtualnet/disconnect', {});
// D31: 兼容别名（CP-001 修复）—— Win 端使用 join/leave 命名，指向同一实现。
// 服务端 /api/virtualnet/connect|disconnect 为主路径，/join|/leave 为 legacy 别名。
export const joinVirtualNet = connectVirtualNet;
export const leaveVirtualNet = disconnectVirtualNet;
export const getVirtualNetNodes = () =>
  apiGet<{ code: string; data: any[] }>('/api/virtualnet/nodes');

// Sounds (出入频道音效)
export const getSounds = () =>
  apiGet<{ code: string; data: { items: UserSound[] } }>('/api/v1/sounds');
export const uploadSound = (
  file: File,
  onProgress?: (loaded: number, total: number) => void,
  signal?: AbortSignal,
) => {
  const formData = new FormData();
  formData.append('file', file);
  return upload<{ code: string; data: UserSound }>('/api/v1/sounds', formData, onProgress, signal);
};
export const deleteSound = (id: string) =>
  apiDelete<{ code: string; data: any }>(`/api/v1/sounds/${id}`);
export const favoriteSound = (id: string) =>
  apiPost<{ code: string; data: any }>(`/api/v1/sounds/${id}/favorite`);
export const unfavoriteSound = (id: string) =>
  apiDelete<{ code: string; data: any }>(`/api/v1/sounds/${id}/favorite`);
export const getSoundSettings = () =>
  apiGet<{ code: string; data: UserSoundSettings }>('/api/v1/users/me/sound-settings');
export const updateSoundSettings = (data: Partial<UserSoundSettings>) =>
  apiPut<{ code: string; data: UserSoundSettings }>('/api/v1/users/me/sound-settings', data);
export const fetchSoundFile = (soundId: string): Promise<Blob> =>
  downloadBlob(`/api/v1/sounds/${soundId}/file`);

export const reportVirtualNetIP = (ip: string) =>
  apiPost<{ code: string; data: any }>('/api/virtualnet/report-ip', { ip });

// D32: 跨域获取网易云 API 基址。Web 端 API_BASE 为空时退化为相对路径；
// Win/Electron 端 API_BASE 指向后端代理时返回完整 URL。
const getNeteaseApiBase = () => `${API_BASE_STATE.value}/api/v1/bots/netease`;

async function neteaseFetch<T>(path: string, method: 'GET' | 'POST' = 'GET', body?: any): Promise<T> {
  const options: RequestInit = { method, credentials: 'include' };
  const headers: Record<string, string> = {};
  const token = localStorage.getItem(TOKEN_KEY);
  if (token) headers['Authorization'] = `Bearer ${token}`;
  if (method === 'POST' && body) {
    headers['Content-Type'] = 'application/json';
    options.body = JSON.stringify(body);
  }
  if (Object.keys(headers).length > 0) options.headers = headers;
  const res = await fetch(`${getNeteaseApiBase()}${path}`, options);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err: any = new Error(data.chinese_message || data.message || `HTTP ${res.status}`);
    const legacyVerification = data.code === 'BOT_NETEASE_API_ERROR'
      && typeof data.message === 'string' && data.message.includes('"code":-462');
    err.code = legacyVerification ? 'BOT_NETEASE_VERIFICATION_REQUIRED' : (data.code || '');
    err.details = data.details || '';
    throw err;
  }
  // Unwrap server's {code:"OK", data:...} wrapper if present
  const inner = data?.data ?? data;
  if (inner && typeof inner.code === 'number' && inner.code !== 200) {
    const err: any = new Error(inner.msg || inner.message || `Netease API error: ${inner.code}`);
    err.code = inner.code;
    throw err;
  }
  return inner as T;
}

// Netease Cloud Music
// D34: 路径中的 uid/id 等参数使用 encodeURIComponent 编码（Win 端对中文歌名等做了编码）
export const neteaseSearch = (keyword: string, limit = 30, offset = 0) =>
  neteaseFetch<{ code: number; result: { songs: any[]; songCount: number } }>(`/search?keywords=${encodeURIComponent(keyword)}&limit=${limit}&offset=${offset}`);
export const neteaseGetPlaylists = (uid: string) =>
  neteaseFetch<{ code: number; playlist: any[] }>(`/playlists?uid=${encodeURIComponent(uid)}`);
export const neteaseGetPlaylistDetail = (id: string) =>
  neteaseFetch<{ code: number; playlist: { tracks: any[] } }>(`/playlist/${encodeURIComponent(id)}`);
export const neteaseGetRecommend = () =>
  neteaseFetch<{ code: number; data: { dailySongs: any[] } }>('/recommend/songs');
export const neteaseGetSongUrl = (id: string) =>
  neteaseFetch<{ code: number; data: { id: number; url: string }[] }>(`/song/${encodeURIComponent(id)}/url`);
export const neteaseGetLyric = (id: string) =>
  neteaseFetch<{ code: number; lrc?: { lyric: string }; tlyric?: { lyric: string } }>(`/lyric/${encodeURIComponent(id)}`);

export const neteaseGetQRKey = () =>
  neteaseFetch<{ code: number; data: { unikey: string } }>('/login/qr/key');
export const neteaseGetQRCreate = (key: string) =>
  neteaseFetch<{ code: number; data: { qrurl: string; qrimg: string } }>(`/login/qr/create?key=${key}&qrimg=true`);
export const neteaseGetQRCheck = (key: string) =>
  neteaseFetch<{ code: number; message: string; cookie?: string }>(`/login/qr/check?key=${key}&timestamp=${Date.now()}`);
export const neteaseGetLoginStatus = () =>
  neteaseFetch<{ code: number; data: { profile?: any } }>('/login/status');
export const neteaseGetUserAccount = () =>
  neteaseFetch<{ code: number; account?: any; profile?: any }>('/user/account');
export const neteaseLogout = () =>
  neteaseFetch<{ code: number }>('/logout', 'POST');
