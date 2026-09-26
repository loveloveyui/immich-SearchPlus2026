<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch, computed } from "vue";
import FaceCompare from "./components/FaceCompare.vue";
import { NConfigProvider, darkTheme, NColorPicker, NSlider, NSwitch, NModal } from "naive-ui";
import {
  Settings,
  AlertTriangle,
  Sun,
  Moon,
  Monitor,
  Image as ImageIcon,
  User,
  Users,
  Crop,
  Check,
  Zap,
  Target,
  Clock,
  RotateCw,
  SlidersHorizontal,
  KeyRound,
  ShieldCheck,
  ShieldAlert,
  X as CloseIcon,
  Trash2,
  ExternalLink,
} from "lucide-vue-next";
import {
  getCachedThumbnail,
  setCachedThumbnail,
  getCachedQuery,
  setCachedQuery,
  deleteCachedQuery,
  clearAllQueryCache,
} from "./utils/cacheDb";
// @ts-ignore
import heic2any from "heic2any";


interface FaceBoundingBox {
  x1: number;
  y1: number;
  x2: number;
  y2: number;
}

interface DetectedFace {
  index: number;
  score: number;
  boundingBox: FaceBoundingBox;
}

interface SearchResult {
  assetId: string;
  similarity: number;
  similarity_pct: number;
  originalFileName: string;
  date_str: string;
  immich_url: string;
}

interface SearchResponse {
  mode: "clip" | "face";
  cost_ms: number;
  cache_hit: boolean;
  detected_faces: DetectedFace[];
  selected_face_index: number;
  results: SearchResult[];
  authenticated_users: Array<{ id: string; name: string; email: string }>;
}

interface ApiKeyRecord {
  id: string;
  label: string;
  key: string;
  enabled: boolean;
  userName?: string;
  isValid?: boolean;
  isTesting?: boolean;
  errorMsg?: string;
}

interface CookieUser {
  id: string;
  name: string;
  email: string;
}

type ThemeMode = "light" | "dark" | "auto";
type SearchMode = "clip" | "face" | "compare";
type SliderType = "threshold" | "topK" | "minScore";

declare const __BUILD_TIME__: string;
const formatBuildTime = (raw: string) => {
  if (!raw || raw === "Dev") return "Dev";
  try {
    const d = new Date(raw);
    if (isNaN(d.getTime())) return raw;
    const pad = (n: number) => n.toString().padStart(2, "0");
    return `${d.getFullYear()}.${pad(d.getMonth() + 1)}.${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
  } catch {
    return raw;
  }
};
const buildTime = typeof __BUILD_TIME__ !== "undefined" ? formatBuildTime(__BUILD_TIME__) : "Dev";

// 客户端硬件加速轻量化降采样
async function convertHeicToJpeg(file: File): Promise<File> {
  const isHeic =
    file.type.includes("heic") ||
    file.type.includes("heif") ||
    /\.(heic|heif)$/i.test(file.name);
  if (!isHeic) return file;
  try {
    const res = await heic2any({ blob: file, toType: "image/jpeg", quality: 0.9 });
    const blob = Array.isArray(res) ? res[0] : res;
    return new File([blob], file.name.replace(/\.[^/.]+$/, "") + ".jpg", { type: "image/jpeg" });
  } catch {
    return file;
  }
}

async function computeImageHash(file: File): Promise<string> {
  const buffer = await file.arrayBuffer();
  if (typeof window !== "undefined" && window.crypto && window.crypto.subtle && typeof window.crypto.subtle.digest === "function") {
    try {
      const hashBuffer = await window.crypto.subtle.digest("SHA-256", buffer);
      return Array.from(new Uint8Array(hashBuffer))
        .map((b) => b.toString(16).padStart(2, "0"))
        .join("");
    } catch (_) {}
  }
  const bytes = new Uint8Array(buffer);
  let h1 = 0xdeadbeef ^ bytes.length;
  let h2 = 0x41c64e6d ^ bytes.length;
  const step = Math.max(1, Math.floor(bytes.length / 2048));
  for (let i = 0; i < bytes.length; i += step) {
    const byte = bytes[i];
    h1 = Math.imul(h1 ^ byte, 2654435761);
    h2 = Math.imul(h2 ^ byte, 1597334677);
  }
  h1 = Math.imul(h1 ^ (h1 >>> 16), 2246822507) ^ Math.imul(h2 ^ (h2 >>> 13), 3266489909);
  h2 = Math.imul(h2 ^ (h2 >>> 16), 2246822507) ^ Math.imul(h1 ^ (h1 >>> 13), 3266489909);
  const p1 = (h1 >>> 0).toString(16).padStart(8, "0");
  const p2 = (h2 >>> 0).toString(16).padStart(8, "0");
  return `${p1}${p2}${bytes.length.toString(16)}`;
}

const clientSearchCache = new Map<string, SearchResponse>();
const forceFreshNext = ref(false);
let authInitPromise: Promise<void> | null = null;

async function ensureAuthReady() {
  if (isCookieChecked.value) return;
  if (!authInitPromise) {
    authInitPromise = (async () => {
      try {
        await checkCookieAuth();
      } finally {
        isCookieChecked.value = true;
      }
    })();
  }
  await authInitPromise;
}

async function retryForceFresh() {
  forceFreshNext.value = true;
  await triggerSearch();
}
const thumbnailBlobUrls = ref<Record<string, string>>({});
const loadedThumbnails = ref<Record<string, boolean>>({});
const thumbnailLoadingSet = new Set<string>();

function onThumbnailLoaded(assetId: string, e: Event) {
  const target = e.target as HTMLImageElement;
  if (!target || target.naturalWidth <= 1 || target.src.startsWith("data:")) {
    return;
  }
  loadedThumbnails.value[assetId] = true;
}

function onThumbnailError(assetId: string, e: Event) {
  const target = e.target as HTMLImageElement;
  if (!target || target.src.startsWith("data:")) return;
  loadedThumbnails.value[assetId] = true;
  target.style.opacity = "0.25";
}
const TRANSPARENT_PIXEL =
  "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7";

async function ensureThumbnailsLoaded(items: SearchResult[]) {
  if (!items || items.length === 0) return;
  const tasks = items.map(async (item) => {
    const id = item.assetId;
    if (thumbnailBlobUrls.value[id] || thumbnailLoadingSet.has(id)) return;
    thumbnailLoadingSet.add(id);
    try {
      const cachedBlob = await getCachedThumbnail(id);
      if (cachedBlob) {
        thumbnailBlobUrls.value[id] = URL.createObjectURL(cachedBlob);
        return;
      }
      const res = await fetch(getThumbnailUrl(id), { credentials: "include" });
      if (res.ok) {
        const blob = await res.blob();
        await setCachedThumbnail(id, blob);
        thumbnailBlobUrls.value[id] = URL.createObjectURL(blob);
      }
    } catch {
    } finally {
      thumbnailLoadingSet.delete(id);
    }
  });
  await Promise.all(tasks);
}

async function preprocessImageClient(file: File, mode: SearchMode): Promise<File> {
  try {
    const normalizedFile = await convertHeicToJpeg(file);
    const bitmap = await createImageBitmap(normalizedFile);
    const origW = bitmap.width;
    const origH = bitmap.height;
    const maxDim = mode === "clip" ? 512 : 640;

    let targetW = origW;
    let targetH = origH;
    if (Math.max(origW, origH) > maxDim) {
      if (origW > origH) {
        targetW = maxDim;
        targetH = Math.round((origH * maxDim) / origW);
      } else {
        targetH = maxDim;
        targetW = Math.round((origW * maxDim) / origH);
      }
    }

    targetW = Math.max(32, Math.round(targetW / 32) * 32);
    targetH = Math.max(32, Math.round(targetH / 32) * 32);
    const canvas = document.createElement("canvas");
    canvas.width = targetW;
    canvas.height = targetH;
    const ctx = canvas.getContext("2d");
    if (!ctx) {
      bitmap.close();
      return file;
    }
    ctx.drawImage(bitmap, 0, 0, targetW, targetH);
    bitmap.close();

    const blob = await new Promise<Blob | null>((resolve) =>
      canvas.toBlob(resolve, "image/jpeg", 0.85)
    );
    if (!blob) return file;
    return new File([blob], file.name.replace(/\.[^/.]+$/, "") + ".jpg", { type: "image/jpeg" });
  } catch {
    return file;
  }
}

// 动态适配子路径部署与开发环境
const getApiBase = () => {
  if (import.meta.env.DEV) return "http://localhost:1880";
  const base = import.meta.env.BASE_URL || "";
  return base.endsWith("/") ? base.slice(0, -1) : base;
};
const API_BASE = getApiBase();

function triggerWarmup(mode: string) {
  const query = activeKeysJoined.value ? `&api_key=${encodeURIComponent(activeKeysJoined.value)}` : "";
  fetch(`${API_BASE}/api/warmup?mode=${mode}${query}`, { credentials: "include" }).catch(() => {});
}

// ================= 状态管理 =================
const theme = ref<ThemeMode>(
  (localStorage.getItem("immich_theme_pref") as ThemeMode) || "auto",
);
const showSettingsModal = ref<boolean>(false);
const showParamModal = ref<boolean>(false);
const showSystemSettingsModal = ref<boolean>(false);
const isClearingCache = ref<boolean>(false);
const clearCacheNotice = ref<string>("");

async function handleClearSearchCache() {
  isClearingCache.value = true;
  clearCacheNotice.value = "";
  try {
    clientSearchCache.clear();
    await clearAllQueryCache();
    clearCacheNotice.value = "检索缓存已彻底清空";
    setTimeout(() => {
      clearCacheNotice.value = "";
    }, 2500);
  } catch {
    clearCacheNotice.value = "清空缓存失败";
  } finally {
    isClearingCache.value = false;
  }
}
const searchMode = ref<SearchMode>(
  (localStorage.getItem("immich_search_mode") as SearchMode) || "clip",
);
const topK = ref<number>(Number(localStorage.getItem("immich_top_k")) || 24);
const threshold = ref<number>(
  Number(localStorage.getItem("immich_threshold")) || 0,
);
const minScore = ref<number>(
  Number(localStorage.getItem("immich_min_score")) || 0.2,
);

const isFaceComparing = ref(false);
const isTaskBusy = computed(() => isLoading.value || isFaceComparing.value);
const isStandaloneCompare = ref(
  new URLSearchParams(window.location.search).get("app") === "compare"
);
if (isStandaloneCompare.value) {
  searchMode.value = "compare";
}

// Immich Cookie 登录态
const showColorModal = ref(false);
let titleClicks = 0;
let titleTimer: ReturnType<typeof setTimeout> | null = null;
function handleTitleClick() {
  if (titleClicks === 0) {
    titleTimer = setTimeout(() => {
      titleClicks = 0;
    }, 10000);
  }
  titleClicks++;
  if (titleClicks >= 5) {
    showColorModal.value = true;
    titleClicks = 0;
    if (titleTimer) clearTimeout(titleTimer);
  }
}

const thresholdMarks = { 0: "0%", 30: "30%", 60: "60%", 85: "85%" };
const topKMarks = { 6: "6", 24: "24", 48: "48", 60: "60" };
const minScoreMarks = { 0.1: "0.1", 0.2: "0.2", 0.4: "0.4", 0.7: "0.7" };

const isEmbedded = ref(
  new URLSearchParams(window.location.search).get("embedded") === "true"
);

function closeEmbeddedSearch() {
  window.parent.postMessage({ type: "IMMICH_CLOSE_SEARCH" }, "*");
}

function getNativeViewerUrl(item: SearchResult): string {
  const path = item.immich_url || `/photos/${encodeURIComponent(item.assetId)}`;
  if (path.startsWith("http://") || path.startsWith("https://")) return path;
  return `${window.location.origin}${path.startsWith("/") ? "" : "/"}${path}`;
}

function getTimelineUrl(item: SearchResult): string {
  return `${window.location.origin}/photos?at=${encodeURIComponent(item.assetId)}`;
}

function openInExternalBrowser(url: string, e?: Event) {
  if (e) {
    e.preventDefault();
    e.stopPropagation();
  }
  if (!url) return;

  const targetUrl = url.startsWith("http://") || url.startsWith("https://")
    ? url
    : `${window.location.origin}${url.startsWith("/") ? "" : "/"}${url}`;

  // 判断是否处于 Android 原生容器内
  const isAndroidApp =
    typeof window !== "undefined" &&
    (!!(window as any).WTAShareInbox ||
     !!(window as any).wtaShareInbox ||
     /Android/i.test(navigator.userAgent));

  if (isAndroidApp) {
    try {
      const parsed = new URL(targetUrl);
      const scheme = parsed.protocol.replace(":", "");
      const hostAndPath = parsed.host + parsed.pathname + parsed.search + parsed.hash;
      // 构造 Android 标准 BROWSABLE Intent，穿透 WebView 呼起系统默认浏览器
      window.location.href = `intent://${hostAndPath}#Intent;scheme=${scheme};action=android.intent.action.VIEW;category=android.intent.category.BROWSABLE;end`;
      return;
    } catch (_) {}
  }

  // PC 端或普通浏览器环境：常规新标签打开
  window.open(targetUrl, "_blank", "noopener,noreferrer");
}


function onParentMessage(e: MessageEvent) {
  if (e.data?.type === "IMMICH_CLOSE_LIGHTBOX") {
    activeLightboxItem.value = null;
    return;
  }
  if (e.data?.type === "IMMICH_THEME_CHANGE") {
    applyTheme(e.data.isDark ? "dark" : "light");
    return;
  }
  if (e.data?.type === "IMMICH_PASTE_IMAGE" && e.data.buffer) {
    if (isTaskBusy.value) return;
    try {
      const blob = new Blob([e.data.buffer], {
        type: e.data.mimeType || "image/jpeg",
      });
      const file = new File(
        [blob],
        e.data.fileName || "clipboard.jpg",
        { type: blob.type }
      );
      handleNewFile(file);
    } catch {}
  }
}

function onPopState() {
  if (isEmbedded.value) return;
  if (expectingStandaloneLightboxPop) {
    expectingStandaloneLightboxPop = false;
    return;
  }
  if (activeLightboxItem.value) {
    hasStandaloneLightboxHistory = false;
    activeLightboxItem.value = null;
  }
}

const isDarkTheme = ref(false);
const naiveTheme = computed(() => (isDarkTheme.value ? darkTheme : null));
const primaryColor = ref(localStorage.getItem("immich_theme_color") || "#10b981");
function hexToRgba(hex: string, alpha: number): string {
  let cleanHex = hex.replace("#", "").trim();
  if (cleanHex.length === 3) {
    cleanHex = cleanHex.split("").map((c) => c + c).join("");
  }
  const num = parseInt(cleanHex, 16);
  if (isNaN(num) || cleanHex.length !== 6) return `rgba(16, 185, 129, ${alpha})`;
  const r = (num >> 16) & 255;
  const g = (num >> 8) & 255;
  const b = num & 255;
  return `rgba(${r}, ${g}, ${b}, ${alpha})`;
}
function applyPrimaryColor(color: string) {
  primaryColor.value = color;
  localStorage.setItem("immich_theme_color", color);
  document.documentElement.style.setProperty("--md-primary", color);
  const containerAlpha = isDarkTheme.value ? 0.18 : 0.12;
  document.documentElement.style.setProperty("--md-primary-container", hexToRgba(color, containerAlpha));
}
watch(primaryColor, (c) => applyPrimaryColor(c));
const themeOverrides = computed(() => ({
  common: {
    primaryColor: isEmbedded.value
      ? (isDarkTheme.value ? "#3b82f6" : "#4250af")
      : primaryColor.value,
    primaryColorHover: primaryColor.value,
    primaryColorPressed: primaryColor.value,
    primaryColorSuppl: primaryColor.value,
    borderRadius: "10px",
  },
}));

const cookieUser = ref<CookieUser | null>(null);
const isCookieChecked = ref<boolean>(false);

function initKeyRecords(): ApiKeyRecord[] {
  const stored = localStorage.getItem("immich_api_keys_v2");
  if (stored) {
    try {
      return JSON.parse(stored);
    } catch {}
  }
  return [];
}

const keyRecords = ref<ApiKeyRecord[]>(initKeyRecords());
const activeKeys = computed(() =>
  keyRecords.value.filter((k) => k.enabled && k.key.trim().length > 0),
);
const activeKeysJoined = computed(() =>
  activeKeys.value.map((k) => k.key.trim()).join(","),
);

// 只要具备 Cookie 会话或至少配置了一个 API Key 即拥有检索授权
const hasAuth = computed(
  () => !!cookieUser.value || activeKeys.value.length > 0,
);

// 运行时状态
const originalImageFile = ref<File | null>(null);
const activeQueryFile = ref<File | null>(null);
const showShareChoiceModal = ref<boolean>(false);
const sharedIncomingFile = ref<File | null>(null);
const sharedIncomingPreviewUrl = ref<string>("");

async function processSharedFile(rawFile: File) {
  if (!rawFile) return;
  // 关键：对三星相册分享的 HEIC 图片执行解码转码，彻底避免 <img> 裂图
  const file = await convertHeicToJpeg(rawFile);
  sharedIncomingFile.value = file;
  if (sharedIncomingPreviewUrl.value) {
    URL.revokeObjectURL(sharedIncomingPreviewUrl.value);
  }
  sharedIncomingPreviewUrl.value = URL.createObjectURL(file);
  showShareChoiceModal.value = true;
}

async function parseWtaItem(item: any): Promise<File | null> {
  if (!item) return null;
  if (item instanceof File) return item;
  if (item instanceof Blob) {
    return new File([item], (item as any).name || "shared.jpg", { type: item.type || "image/jpeg" });
  }
  if (typeof item === "string") {
    try {
      const parsed = JSON.parse(item);
      return parseWtaItem(parsed);
    } catch (_) {}
  }

  const name = item.name || "shared.jpg";
  const mime = item.mimeType || item.type || "image/jpeg";

  // 1. WebToApp 官方规范：<= 5MB 内联 dataUrl 字段
  const dataUrl = item.dataUrl || item.base64 || item.data || item.content;
  if (dataUrl && typeof dataUrl === "string") {
    try {
      const formattedUrl = dataUrl.startsWith("data:")
        ? dataUrl
        : `data:${mime};base64,${dataUrl}`;
      const res = await fetch(formattedUrl);
      const blob = await res.blob();
      return new File([blob], name, { type: mime || blob.type || "image/jpeg" });
    } catch (_) {}
  }

  // 2. WebToApp 官方规范：> 5MB 设备本地文件 fileUrl 字段
  const fileUrl = item.fileUrl || item.url || item.uri || item.path;
  if (fileUrl && typeof fileUrl === "string") {
    try {
      const res = await fetch(fileUrl);
      const blob = await res.blob();
      return new File([blob], name, { type: mime || blob.type || "image/jpeg" });
    } catch (_) {}
  }

  // 3. 原生方法兜底
  if (typeof item.getFile === "function") {
    try {
      const f = await item.getFile();
      if (f instanceof File) return f;
    } catch (_) {}
  }
  if (typeof item.blob === "function") {
    try {
      const b = await item.blob();
      if (b) return new File([b], name, { type: mime || b.type || "image/jpeg" });
    } catch (_) {}
  }
  if (typeof item.arrayBuffer === "function") {
    try {
      const buf = await item.arrayBuffer();
      if (buf) return new File([buf], name, { type: mime || "image/jpeg" });
    } catch (_) {}
  }

  return null;
}

function isWtaItemConsumed(id: string): boolean {
  if (!id) return false;
  try {
    const records = JSON.parse(localStorage.getItem("wta_consumed_share_ids") || "[]");
    return Array.isArray(records) && records.includes(id);
  } catch {
    return false;
  }
}

function markWtaItemConsumed(id: string) {
  if (!id) return;
  try {
    let records = JSON.parse(localStorage.getItem("wta_consumed_share_ids") || "[]");
    if (!Array.isArray(records)) records = [];
    if (!records.includes(id)) {
      records.push(id);
      if (records.length > 50) records = records.slice(-50); // 仅保留最近 50 条
      localStorage.setItem("wta_consumed_share_ids", JSON.stringify(records));
    }
  } catch {}
}

async function handleWtaSharedList(rawList: any) {
  if (!rawList) return;
  let list: any[] = [];
  if (typeof rawList === "string") {
    try {
      const parsed = JSON.parse(rawList);
      list = Array.isArray(parsed) ? parsed : [parsed];
    } catch (_) {
      list = [rawList];
    }
  } else if (Array.isArray(rawList)) {
    list = rawList;
  } else {
    list = [rawList];
  }

  for (const raw of list) {
    // 提取 WTA 专有唯一 ID 或特征指纹
    const shareId = raw?.id || (raw?.name ? `${raw.name}_${raw.size}_${raw.mimeType || raw.type || ''}` : "");
    if (shareId && isWtaItemConsumed(shareId)) {
      // 该分享之前已弹出处理或取消过，忽略 WTA 启动时的重新投递
      continue;
    }

    const file = await parseWtaItem(raw);
    if (file && (file.type.startsWith("image/") || file.name.match(/\.(jpe?g|png|webp|gif|heic|heif)$/i))) {
      if (shareId) {
        markWtaItemConsumed(shareId);
      }
      await processSharedFile(file);
      break; // 严格只取第一张
    }
  }
}

function initWtaShareBridge() {
  const consumeInbox = async () => {
    const wta = (window as any).WTAShareInbox || (window as any).wtaShareInbox;
    if (wta) {
      try {
        if (typeof wta.take === "function") {
          const taken = await wta.take();
          if (taken && (taken.length > 0 || (typeof taken === "string" && taken.trim().length > 0))) {
            await handleWtaSharedList(taken);
            return;
          }
        }
        if (typeof wta.peek === "function") {
          const peeked = await wta.peek();
          if (peeked && peeked.length > 0) {
            if (typeof wta.take === "function") await wta.take();
            await handleWtaSharedList(peeked);
            return;
          }
        }
      } catch (_) {}
    }
  };

  try {
    const wta = (window as any).WTAShareInbox || (window as any).wtaShareInbox;
    if (wta && typeof wta.onShare === "function") {
      wta.onShare((items: any) => {
        handleWtaSharedList(items);
      });
    }
  } catch (_) {}

  const onShareEvent = (e: any) => {
    const items = e?.detail?.items || e?.detail?.files || e?.detail;
    if (items) {
      handleWtaSharedList(items);
    } else {
      consumeInbox();
    }
  };

  window.addEventListener("wta:share", onShareEvent);
  document.addEventListener("wta:share", onShareEvent);

  consumeInbox();
  let attempts = 0;
  const timer = setInterval(() => {
    attempts++;
    consumeInbox();
    try {
      const wta = (window as any).WTAShareInbox || (window as any).wtaShareInbox;
      if (wta && typeof wta.onShare === "function") {
        wta.onShare((items: any) => {
          handleWtaSharedList(items);
        });
      }
    } catch (_) {}
    if (attempts >= 20 || sharedIncomingFile.value) {
      clearInterval(timer);
    }
  }, 100);
}

async function checkPwaSharedImage() {
  if (typeof window === "undefined" || !("caches" in window)) return;
  const url = new URL(window.location.href);
  if (url.searchParams.get("shared") === "1") {
    url.searchParams.delete("shared");
    window.history.replaceState(window.history.state, "", url.pathname + (url.search ? url.search : "") + url.hash);
    try {
      const cache = await caches.open("searchplus2026-share-cache");
      const res = await cache.match("/_pwa_shared_media_");
      if (res) {
        const blob = await res.blob();
        const filename = decodeURIComponent(res.headers.get("x-shared-name") || "shared.jpg");
        await cache.delete("/_pwa_shared_media_");
        const file = new File([blob], filename, { type: blob.type || "image/jpeg" });
        await processSharedFile(file);
      }
    } catch {}
  }
}

function applyShareChoice(mode: SearchMode) {
  showShareChoiceModal.value = false;
  if (!sharedIncomingFile.value) return;
  searchMode.value = mode;
  handleNewFile(sharedIncomingFile.value);
  sharedIncomingFile.value = null;
}

function cancelShareChoice() {
  showShareChoiceModal.value = false;
  sharedIncomingFile.value = null;
  if (sharedIncomingPreviewUrl.value) {
    URL.revokeObjectURL(sharedIncomingPreviewUrl.value);
    sharedIncomingPreviewUrl.value = "";
  }
}
const selectedFaceIndex = ref<number>(-1);
const isLoading = ref<boolean>(false);
const errorMessage = ref<string>("");
const queryPreviewUrl = ref<string>("");
const queryFileName = ref<string>("");
const searchCostMs = ref<number>(0);
const isCacheHit = ref<boolean>(false);
const allRawResults = ref<SearchResult[]>([]);
const detectedFaces = ref<DetectedFace[]>([]);
const fileInputRef = ref<HTMLInputElement | null>(null);
const isDragging = ref<boolean>(false);
const activeLightboxItem = ref<SearchResult | null>(null);
const activeLightboxThumbSrc = ref<string>("");
const activeLightboxHdSrc = ref<string>("");
const isHighResLoading = ref<boolean>(false);
const isHighResReady = ref<boolean>(false);
const isLightboxOpening = ref<boolean>(false);
let highResAbortController: AbortController | null = null;
let highResBlobUrlToRevoke: string | null = null;
let hasStandaloneLightboxHistory = false;
let expectingStandaloneLightboxPop = false;

function onLightboxAfterEnter() {
  isLightboxOpening.value = false;
}

async function loadHighResImage(assetId: string) {
  if (highResAbortController) {
    highResAbortController.abort();
  }
  highResAbortController = new AbortController();
  const signal = highResAbortController.signal;

  isHighResLoading.value = true;
  const apiKeyParam = activeKeysJoined.value
    ? `api_key=${encodeURIComponent(activeKeysJoined.value)}`
    : "";

  const candidates = [
    `/api/assets/${encodeURIComponent(assetId)}/thumbnail?size=preview${apiKeyParam ? `&${apiKeyParam}` : ""}`,
    `${API_BASE}/api/thumbnail/${encodeURIComponent(assetId)}?size=preview${apiKeyParam ? `&${apiKeyParam}` : ""}`,
    `/api/assets/${encodeURIComponent(assetId)}/original${apiKeyParam ? `?${apiKeyParam}` : ""}`,
  ];

  for (const url of candidates) {
    if (signal.aborted) return;
    try {
      const headers: Record<string, string> = {};
      if (activeKeysJoined.value) {
        headers["x-api-key"] = activeKeys.value[0]?.key || activeKeysJoined.value;
      }
      const res = await fetch(url, { credentials: "include", headers, signal });
      if (res.ok) {
        const blob = await res.blob();
        if (signal.aborted) return;
          if (blob && blob.size > 1024) {
            if (highResBlobUrlToRevoke) {
              URL.revokeObjectURL(highResBlobUrlToRevoke);
            }
            const newUrl = URL.createObjectURL(blob);
            highResBlobUrlToRevoke = newUrl;
            activeLightboxHdSrc.value = newUrl;
            isHighResLoading.value = false;
            return;
          }

      }
    } catch (e: any) {
      if (e.name === "AbortError") return;
    }
  }

  if (!signal.aborted) {
    isHighResLoading.value = false;
  }
}

watch(activeLightboxItem, (item) => {
  if (isEmbedded.value) {
    window.parent.postMessage(
      { type: item ? "IMMICH_LIGHTBOX_OPEN" : "IMMICH_LIGHTBOX_CLOSE" },
      "*"
    );
  } else {
    if (item) {
      if (!hasStandaloneLightboxHistory) {
        try {
          history.pushState({ __immich_lightbox__: true }, "", window.location.href);
          hasStandaloneLightboxHistory = true;
        } catch (_) {}
      }
    } else if (hasStandaloneLightboxHistory) {
      hasStandaloneLightboxHistory = false;
      expectingStandaloneLightboxPop = true;
      try {
        history.back();
      } catch (_) {
        expectingStandaloneLightboxPop = false;
      }
      setTimeout(() => {
        expectingStandaloneLightboxPop = false;
      }, 300);
    }
  }
  if (highResAbortController) {
    highResAbortController.abort();
    highResAbortController = null;
  }
  if (highResBlobUrlToRevoke) {
    URL.revokeObjectURL(highResBlobUrlToRevoke);
    highResBlobUrlToRevoke = null;
  }
  isHighResReady.value = false;
  activeLightboxHdSrc.value = "";
  if (item) {
    isLightboxOpening.value = true;
    activeLightboxThumbSrc.value =
      thumbnailBlobUrls.value[item.assetId] || getThumbnailUrl(item.assetId);
    loadHighResImage(item.assetId);
  } else {
    isLightboxOpening.value = false;
    activeLightboxThumbSrc.value = "";
    isHighResLoading.value = false;
  }
});

// 选区裁剪
const isCroppingMode = ref<boolean>(false);
const cropStart = ref<{ x: number; y: number } | null>(null);
const cropCurrent = ref<{ x: number; y: number } | null>(null);
const isDrawingBox = ref<boolean>(false);
const heroImgRef = ref<HTMLImageElement | null>(null);
const cropContainerRef = ref<HTMLDivElement | null>(null);
const isSubImageQuery = ref<boolean>(false);

// 客户端 60fps 实时截断
const displayedResults = computed(() => {
  if (!allRawResults.value || allRawResults.value.length === 0) return [];
  return allRawResults.value
    .filter((item) => item.similarity_pct >= threshold.value)
    .slice(0, topK.value);
});

watch(
  displayedResults,
  (items) => {
    ensureThumbnailsLoaded(items);
  },
  { immediate: true }
);

function getSimilarityBadgeStyle(pct: number) {
  const clamped = Math.max(0, Math.min(100, pct));
  const hue = Math.round((clamped / 100) * 125);
  return {
    backgroundColor: `hsla(${hue}, 82%, 35%, 0.88)`,
  };
}

function getThumbnailDisplayUrl(assetId: string): string {
  return thumbnailBlobUrls.value[assetId] || TRANSPARENT_PIXEL;
}

function getThumbnailUrl(assetId: string): string {
  const query = activeKeysJoined.value
    ? `?api_key=${encodeURIComponent(activeKeysJoined.value)}`
    : "";
  return `${API_BASE}/api/thumbnail/${assetId}${query}`;
}

// 瀑布流多列排布
const columnCount = ref<number>(4);
function updateColumnCount() {
  const w = window.innerWidth;
  if (w < 768) columnCount.value = 2;
  else if (w < 1100) columnCount.value = 3;
  else columnCount.value = 4;
}

const masonryColumns = computed(() => {
  const cols: SearchResult[][] = Array.from(
    { length: columnCount.value },
    () => [],
  );
  displayedResults.value.forEach((item, idx) => {
    cols[idx % columnCount.value].push(item);
  });
  return cols;
});

watch(theme, (v) => {
  localStorage.setItem("immich_theme_pref", v);
  applyTheme(v);
});
watch(searchMode, (v) => localStorage.setItem("immich_search_mode", v));
watch(topK, (v) => {
  localStorage.setItem("immich_top_k", v.toString());
  debouncedServerFetch();
});
watch(threshold, (v) => localStorage.setItem("immich_threshold", v.toString()));
watch(minScore, (v) => {
  localStorage.setItem("immich_min_score", v.toString());
  if (activeQueryFile.value && searchMode.value === "face") triggerSearch();
});
watch(
  keyRecords,
  (list) => {
    localStorage.setItem("immich_api_keys_v2", JSON.stringify(list));
  },
  { deep: true },
);

function updateThemeColorMeta(isDark: boolean) {
  const color = isDark ? "#0a0b0d" : "#ffffff";
  document.querySelectorAll('meta[name="theme-color"]').forEach((meta) => {
    meta.setAttribute("content", color);
  });
}

function applyTheme(targetTheme: ThemeMode) {
  let isDark = false;
  if (targetTheme === "auto") {
    isDark = window.matchMedia("(prefers-color-scheme: dark)").matches;
  } else {
    isDark = targetTheme === "dark";
  }
  isDarkTheme.value = isDark;
  document.documentElement.classList.toggle("dark", isDark);
  document.documentElement.style.colorScheme = isDark ? "dark" : "light";
  applyPrimaryColor(primaryColor.value);
  updateThemeColorMeta(isDark);
}

// 检测同域 Cookie 会话
async function checkCookieAuth() {
  try {
    const res = await fetch(`${API_BASE}/api/auth/me`, {
      credentials: "include",
    });
    if (res.ok) {
      const data = await res.json();
      if (data.authenticated && data.user) {
        cookieUser.value = data.user;
      }
    }
  } catch (e) {
    // 跨域或未登录
  } finally {
    isCookieChecked.value = true;
  }
}

// API Key 管理
function addKeyRow() {
  keyRecords.value.push({
    id: "k_" + Date.now(),
    label: `额外账号 ${keyRecords.value.length + 1}`,
    key: "",
    enabled: true,
  });
}

function removeKeyRow(idx: number) {
  keyRecords.value.splice(idx, 1);
  if (activeQueryFile.value && hasAuth.value) triggerSearch();
}

async function testSingleKey(record: ApiKeyRecord) {
  if (!record.key.trim()) {
    record.errorMsg = "请先填写密钥";
    return;
  }
  record.isTesting = true;
  record.errorMsg = "";
  try {
    const res = await fetch(`${API_BASE}/api/auth/validate`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ api_key: record.key.trim() }),
      credentials: "include",
    });
    const data = await res.json();
    if (res.ok && data.valid) {
      record.isValid = true;
      record.userName = data.user.name || data.user.email;
    } else {
      record.isValid = false;
      record.errorMsg = data.detail || "校验失败";
    }
  } catch (e: unknown) {
    record.isValid = false;
    record.errorMsg = e instanceof Error ? e.message : "连接超时";
  } finally {
    record.isTesting = false;
  }
}

// 检索触发
let fetchTimer: ReturnType<typeof setTimeout> | null = null;
function debouncedServerFetch() {
  if (fetchTimer) clearTimeout(fetchTimer);
  fetchTimer = setTimeout(() => {
    if (activeQueryFile.value && topK.value > allRawResults.value.length) {
      triggerSearch();
    }
  }, 300);
}

async function triggerSearch(faceIdxOverride?: number) {
  if (!activeQueryFile.value) return;
  if (!hasAuth.value) {
    errorMessage.value = "未检测到 Immich 登录态，必须至少配置一个 API Key";
    showSettingsModal.value = true;
    return;
  }

  errorMessage.value = "";
  isLoading.value = true;
  if (faceIdxOverride === undefined) {
    allRawResults.value = [];
  }

  const formData = new FormData();
  formData.append("file", activeQueryFile.value);
  formData.append("mode", searchMode.value);
  const fetchTopK = Math.max(topK.value, 60);
  formData.append("top_k", fetchTopK.toString());
  formData.append("threshold", "0");
  formData.append("min_score", minScore.value.toString());
  if (activeKeysJoined.value) {
    formData.append("api_keys", activeKeysJoined.value);
  }

  const targetFace =
    faceIdxOverride !== undefined ? faceIdxOverride : selectedFaceIndex.value;
  formData.append("face_index", targetFace.toString());

  const imageHash = await computeImageHash(activeQueryFile.value);
  const cacheKey = `${imageHash}:${searchMode.value}:${minScore.value}:${targetFace}:${activeKeysJoined.value}`;
  await ensureAuthReady();
  const isForce = forceFreshNext.value;
  forceFreshNext.value = false;
  if (isForce) {
    clientSearchCache.delete(cacheKey);
    await deleteCachedQuery(cacheKey);
  }
  let cached = isForce ? null : clientSearchCache.get(cacheKey);
  if (!cached && !isForce) {
    cached = await getCachedQuery(cacheKey);
    if (cached && Array.isArray(cached.results) && cached.results.length > 0) {
      clientSearchCache.set(cacheKey, cached);
    } else if (cached) {
      await deleteCachedQuery(cacheKey);
      cached = null;
    }
  }
  if (cached && Array.isArray(cached.results) && cached.results.length > 0) {
    allRawResults.value = cached.results || [];
    searchCostMs.value = 0;
    isCacheHit.value = true;
    detectedFaces.value = cached.detected_faces || [];
    selectedFaceIndex.value = cached.selected_face_index ?? -1;
    isLoading.value = false;
    return;
  }
  formData.append("image_hash", imageHash);

  const headers: Record<string, string> = {};
  headers["X-Image-Hash"] = imageHash;
  if (activeKeysJoined.value) {
    headers["X-Immich-Api-Key"] = activeKeysJoined.value;
  }

  try {
    const res = await fetch(`${API_BASE}/api/search`, {
      method: "POST",
      headers,
      body: formData,
      credentials: "include", // 关键：自动透传 Immich 同域 Cookie
    });
    const resText = await res.text();

    let data: SearchResponse & { detail?: string };
    try {
      data = JSON.parse(resText);
    } catch {
      throw new Error(`服务响应异常 [${res.status}]: ${resText || "无内容"}`);
    }

    if (!res.ok) {
      throw new Error(data.detail || `检索失败 (HTTP ${res.status})`);
    }

    allRawResults.value = Array.isArray(data.results) ? data.results : [];
    if (allRawResults.value.length > 0) {
      clientSearchCache.set(cacheKey, data);
      setCachedQuery(cacheKey, data);
    } else {
      clientSearchCache.delete(cacheKey);
      await deleteCachedQuery(cacheKey);
    }
    searchCostMs.value = data.cost_ms || 0;
    isCacheHit.value = !!data.cache_hit;
    detectedFaces.value = Array.isArray(data.detected_faces)
      ? data.detected_faces
      : [];
    selectedFaceIndex.value = data.selected_face_index ?? -1;
  } catch (err: unknown) {
    errorMessage.value = err instanceof Error ? err.message : "检索异常";
    allRawResults.value = [];
  } finally {
    isLoading.value = false;
  }
}

async function handleNewFile(file: File) {
  if (!file.type.startsWith("image/")) {
    errorMessage.value = "请上传图片格式文件 (JPEG/PNG/WebP/GIF)";
    return;
  }
  if (!hasAuth.value) {
    errorMessage.value = "请先登录 Immich 或在设置中配置 API Key";
    showSettingsModal.value = true;
    return;
  }

  isLoading.value = true;
  const readyFile = await preprocessImageClient(file, searchMode.value);
  isLoading.value = false;

  originalImageFile.value = readyFile;
  activeQueryFile.value = readyFile;
  isSubImageQuery.value = false;
  isCroppingMode.value = false;
  queryFileName.value =
    file.name || (searchMode.value === "face" ? "截屏人脸" : "截屏图像");
  queryPreviewUrl.value = URL.createObjectURL(readyFile);
  selectedFaceIndex.value = -1;
  detectedFaces.value = [];
  triggerSearch();
}

function clearCurrentQuery() {
  loadedThumbnails.value = {};
  originalImageFile.value = null;
  activeQueryFile.value = null;
  queryPreviewUrl.value = "";
  queryFileName.value = "";
  allRawResults.value = [];
  detectedFaces.value = [];
  selectedFaceIndex.value = -1;
  isCroppingMode.value = false;
  isSubImageQuery.value = false;
  errorMessage.value = "";
}

function selectFace(idx: number) {
  if (selectedFaceIndex.value === idx || isLoading.value) return;
  selectedFaceIndex.value = idx;
  triggerSearch(idx);
}

watch(searchMode, (newMode) => {
  selectedFaceIndex.value = -1;
  detectedFaces.value = [];
  if (activeQueryFile.value && newMode !== "compare") triggerSearch();
});

// ROI 选区
const cropBoxStyle = computed(() => {
  if (!cropStart.value || !cropCurrent.value) return {};
  const x1 = Math.min(cropStart.value.x, cropCurrent.value.x);
  const y1 = Math.min(cropStart.value.y, cropCurrent.value.y);
  const w = Math.abs(cropCurrent.value.x - cropStart.value.x);
  const h = Math.abs(cropCurrent.value.y - cropStart.value.y);
  return {
    left: `${x1}px`,
    top: `${y1}px`,
    width: `${w}px`,
    height: `${h}px`,
  };
});

function onCropPointerDown(e: PointerEvent) {
  if (!isCroppingMode.value || !cropContainerRef.value) return;
  const rect = cropContainerRef.value.getBoundingClientRect();
  cropStart.value = { x: e.clientX - rect.left, y: e.clientY - rect.top };
  cropCurrent.value = { ...cropStart.value };
  isDrawingBox.value = true;
}

function onCropPointerMove(e: PointerEvent) {
  if (!isDrawingBox.value || !cropContainerRef.value) return;
  const rect = cropContainerRef.value.getBoundingClientRect();
  cropCurrent.value = {
    x: Math.max(0, Math.min(rect.width, e.clientX - rect.left)),
    y: Math.max(0, Math.min(rect.height, e.clientY - rect.top)),
  };
}

function onCropPointerUp() {
  isDrawingBox.value = false;
}

function applyRoiCrop() {
  if (
    !cropStart.value ||
    !cropCurrent.value ||
    !heroImgRef.value ||
    !cropContainerRef.value
  )
    return;
  const img = heroImgRef.value;
  const rect = cropContainerRef.value.getBoundingClientRect();

  const x1 = Math.min(cropStart.value.x, cropCurrent.value.x);
  const y1 = Math.min(cropStart.value.y, cropCurrent.value.y);
  const w = Math.abs(cropCurrent.value.x - cropStart.value.x);
  const h = Math.abs(cropCurrent.value.y - cropStart.value.y);

  if (w < 20 || h < 20) {
    alert("框选区域过小，请重新圈选");
    return;
  }

  const scaleX = img.naturalWidth / rect.width;
  const scaleY = img.naturalHeight / rect.height;

  let cropW = Math.round(w * scaleX);
  let cropH = Math.round(h * scaleY);
  cropW = Math.max(32, Math.round(cropW / 32) * 32);
  cropH = Math.max(32, Math.round(cropH / 32) * 32);
  const canvas = document.createElement("canvas");
  canvas.width = cropW;
  canvas.height = cropH;
  const ctx = canvas.getContext("2d");
  if (!ctx) return;

  ctx.drawImage(
    img,
    x1 * scaleX,
    y1 * scaleY,
    w * scaleX,
    h * scaleY,
    0,
    0,
    canvas.width,
    canvas.height,
  );
  canvas.toBlob(
    (blob) => {
      if (!blob) return;
      activeQueryFile.value = new File([blob], "crop-target.jpg", {
        type: "image/jpeg",
      });
      isSubImageQuery.value = true;
      isCroppingMode.value = false;
      cropStart.value = null;
      cropCurrent.value = null;
      triggerSearch();
    },
    "image/jpeg",
    0.95,
  );
}

function cancelCrop() {
  if (isTaskBusy.value) return;
  isCroppingMode.value = false;
  cropStart.value = null;
  cropCurrent.value = null;
}

function resetToFullImage() {
  if (!originalImageFile.value) return;
  activeQueryFile.value = originalImageFile.value;
  isSubImageQuery.value = false;
  isCroppingMode.value = false;
  cropStart.value = null;
  cropCurrent.value = null;
  triggerSearch();
}

// 触摸滑块防误触
interface TouchDragSession {
  type: SliderType;
  startX: number;
  startY: number;
  trackLeft: number;
  trackWidth: number;
  hasHorizontalLocked: boolean;
  isVerticalScrollDiscarded: boolean;
}

let currentSession: TouchDragSession | null = null;

function getSliderConfig(type: SliderType) {
  if (type === "threshold")
    return { min: 0, max: 85, step: 5, val: threshold.value };
  if (type === "topK") return { min: 6, max: 60, step: 6, val: topK.value };
  return { min: 0.1, max: 0.7, step: 0.05, val: minScore.value };
}

function setSliderVal(type: SliderType, val: number) {
  if (type === "threshold") threshold.value = val;
  else if (type === "topK") topK.value = val;
  else minScore.value = val;
}

function getSliderRatio(type: SliderType): number {
  const c = getSliderConfig(type);
  return Math.max(0, Math.min(1, (c.val - c.min) / (c.max - c.min)));
}

function updateSliderByClientX(
  clientX: number,
  trackLeft: number,
  trackWidth: number,
  type: SliderType,
) {
  const ratio = Math.max(0, Math.min(1, (clientX - trackLeft) / trackWidth));
  const c = getSliderConfig(type);
  let raw = c.min + ratio * (c.max - c.min);
  if (c.step > 0) raw = Math.round(raw / c.step) * c.step;
  setSliderVal(type, Number(raw.toFixed(2)));
}

function onTouchStartSlider(e: TouchEvent, type: SliderType) {
  if (e.touches.length !== 1) return;
  const touch = e.touches[0];
  const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
  currentSession = {
    type,
    startX: touch.clientX,
    startY: touch.clientY,
    trackLeft: rect.left,
    trackWidth: rect.width,
    hasHorizontalLocked: false,
    isVerticalScrollDiscarded: false,
  };
}

function onTouchMoveSlider(e: TouchEvent) {
  if (
    !currentSession ||
    currentSession.isVerticalScrollDiscarded ||
    e.touches.length !== 1
  )
    return;
  const touch = e.touches[0];
  const dx = touch.clientX - currentSession.startX;
  const dy = touch.clientY - currentSession.startY;

  if (!currentSession.hasHorizontalLocked) {
    if (Math.abs(dy) > Math.abs(dx) && Math.abs(dy) > 5) {
      currentSession.isVerticalScrollDiscarded = true;
      return;
    }
    if (Math.abs(dx) > Math.abs(dy) && Math.abs(dx) > 8) {
      currentSession.hasHorizontalLocked = true;
    } else {
      return;
    }
  }

  if (e.cancelable) e.preventDefault();
  updateSliderByClientX(
    touch.clientX,
    currentSession.trackLeft,
    currentSession.trackWidth,
    currentSession.type,
  );
}

function onTouchEndSlider() {
  currentSession = null;
}

function onMouseDownSlider(e: MouseEvent, type: SliderType) {
  const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
  const startX = e.clientX;
  let isDraggingMouse = false;

  const onMouseMove = (ev: MouseEvent) => {
    if (Math.abs(ev.clientX - startX) > 6) isDraggingMouse = true;
    if (isDraggingMouse) {
      updateSliderByClientX(ev.clientX, rect.left, rect.width, type);
    }
  };

  const onMouseUp = () => {
    window.removeEventListener("mousemove", onMouseMove);
    window.removeEventListener("mouseup", onMouseUp);
  };

  window.addEventListener("mousemove", onMouseMove);
  window.addEventListener("mouseup", onMouseUp);
}

function onDrop(e: DragEvent) {
  e.preventDefault();
  isDragging.value = false;
  if (isTaskBusy.value) return;
  if (e.dataTransfer?.files && e.dataTransfer.files.length > 0) {
    handleNewFile(e.dataTransfer.files[0]);
  }
}

function onFileSelect(e: Event) {
  const target = e.target as HTMLInputElement;
  if (target.files && target.files.length > 0) {
    handleNewFile(target.files[0]);
    target.value = "";
  }
}

function onPaste(e: ClipboardEvent) {
  if (isTaskBusy.value) return;
  // 比对模式下由 FaceCompare 组件独立接管粘贴目标
  if (searchMode.value === "compare") return;

  const items = e.clipboardData?.items;
  if (!items) return;
  for (const item of items) {
    if (item.type.startsWith("image/")) {
      const blob = item.getAsFile();
      if (blob) handleNewFile(blob);
      break;
    }
  }
}

const systemDarkMedia = window.matchMedia("(prefers-color-scheme: dark)");
function onSystemThemeChange() {
  if (theme.value === "auto") applyTheme("auto");
}

onMounted(async () => {
  applyTheme(theme.value);
  applyPrimaryColor(primaryColor.value);
  updateColumnCount();
  window.addEventListener("resize", updateColumnCount);
  window.addEventListener("paste", onPaste);
  window.addEventListener("message", onParentMessage);
  window.addEventListener("popstate", onPopState);
  systemDarkMedia.addEventListener("change", onSystemThemeChange);
  if (isEmbedded.value) {
    const urlTheme = new URLSearchParams(window.location.search).get("theme");
    if (urlTheme) {
      applyTheme(urlTheme === "dark" ? "dark" : "light");
    }
  }
  initWtaShareBridge();
  await checkCookieAuth();
  if (!hasAuth.value) {
    showSettingsModal.value = true;
  }
  await checkPwaSharedImage();
});

onUnmounted(() => {
  window.removeEventListener("resize", updateColumnCount);
  window.removeEventListener("paste", onPaste);
  window.removeEventListener("message", onParentMessage);
  window.removeEventListener("popstate", onPopState);
  systemDarkMedia.removeEventListener("change", onSystemThemeChange);
});
</script>

<template>
  <n-config-provider :theme="naiveTheme" :theme-overrides="themeOverrides">
  <div class="app-wrapper" :class="{ 'is-embedded': isEmbedded, 'is-dark': isDarkTheme }">
    <div class="container">
      <div v-if="isEmbedded" class="embedded-nav-bar">
        <div class="embedded-title-group">
          <svg class="windmill-icon" :width="16" :height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
            <path d="M12 12V3.5c2.4 0 4.3 1.9 4.3 4.3S14.4 12 12 12z" fill="currentColor" fill-opacity="0.25" />
            <path d="M12 12h8.5c0 2.4-1.9 4.3-4.3 4.3S12 14.4 12 12z" fill="currentColor" fill-opacity="0.25" />
            <path d="M12 12v8.5c-2.4 0-4.3-1.9-4.3-4.3S9.6 12 12 12z" fill="currentColor" fill-opacity="0.25" />
            <path d="M12 12H3.5c0-2.4 1.9-4.3 4.3-4.3S12 9.6 12 12z" fill="currentColor" fill-opacity="0.25" />
            <circle cx="12" cy="12" r="2" fill="currentColor" />
          </svg>
          <div class="embedded-title-col">
            <span class="embedded-title-text">SearchPlus2026 智能检索</span>
            <span class="badge-engine embedded-badge">Build {{ buildTime }}</span>
          </div>
        </div>
        <button
          class="embedded-close-btn"
          @click="closeEmbeddedSearch"
          title="关闭并返回相册 (Esc)"
        >
          <CloseIcon :size="14" />
          <span>返回相册</span>
        </button>
      </div>
      <header v-if="!isEmbedded" class="app-header">
        <div class="brand-cluster">
          <div class="brand-avatar" title="Immich 视觉检索">
            <svg class="windmill-icon" :width="22" :height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
              <path d="M12 12V3.5c2.4 0 4.3 1.9 4.3 4.3S14.4 12 12 12z" fill="currentColor" fill-opacity="0.25" />
              <path d="M12 12h8.5c0 2.4-1.9 4.3-4.3 4.3S12 14.4 12 12z" fill="currentColor" fill-opacity="0.25" />
              <path d="M12 12v8.5c-2.4 0-4.3-1.9-4.3-4.3S9.6 12 12 12z" fill="currentColor" fill-opacity="0.25" />
              <path d="M12 12H3.5c0-2.4 1.9-4.3 4.3-4.3S12 9.6 12 12z" fill="currentColor" fill-opacity="0.25" />
              <circle cx="12" cy="12" r="2" fill="currentColor" />
            </svg>
          </div>
          <div class="brand-text">
            <div class="title-row">
              <h1 class="brand-title" @click="handleTitleClick" style="cursor: pointer; user-select: none;" title="10秒内连续点击5次开启调色">{{ isStandaloneCompare ? "SearchPlus2026 人脸比对中心" : "SearchPlus2026" }}</h1>
              <span class="badge-engine">Build {{ buildTime }}</span>
            </div>
            <p class="brand-subtitle">
              SigLIP 全局语义 · InsightFace 精准人脸 · 同域登录无感直通
            </p>
          </div>
        </div>

        <div class="header-actions">
          <!-- 登录状态/API Key 汇总胶囊 -->
          <button
            class="settings-badge-btn"
            :class="{ warning: !hasAuth }"
            @click="showSettingsModal = true"
          >
            <template v-if="cookieUser">
              <span>👤 {{ cookieUser.name }}</span>
              <span class="active-count-tag">会话直通</span>
              <span class="active-count-tag" v-if="activeKeys.length > 0"
                >+{{ activeKeys.length }} 外部Key</span
              >
            </template>
            <template v-else-if="hasAuth">
              <KeyRound :size="13" />
              <span>已启用 {{ activeKeys.length }} 密钥</span>
            </template>
            <template v-else>
              <Settings :size="13" />
              <span>凭据配置</span>
              <span class="active-count-tag red"><AlertTriangle :size="10" /> 需配置Key</span>
            </template>
          </button>

          <div class="theme-segmented">
            <button
              class="seg-item"
              :class="{ active: theme === 'light' }"
              @click="theme = 'light'"
              title="浅色模式"
            >
              <Sun :size="13" />
            </button>
            <button
              class="seg-item"
              :class="{ active: theme === 'dark' }"
              @click="theme = 'dark'"
              title="深色模式"
            >
              <Moon :size="13" />
            </button>
            <button
              class="seg-item"
              :class="{ active: theme === 'auto' }"
              @click="theme = 'auto'"
              title="跟随系统"
            >
              <Monitor :size="13" />
            </button>
            <button
              class="seg-item"
              @click="showSystemSettingsModal = true"
              title="系统设置"
            >
              <Settings :size="13" />
            </button>
          </div>
          <n-modal v-model:show="showColorModal" preset="card" title="🎨 自定义界面配色" style="max-width: 320px; border-radius: 16px;">
            <n-color-picker
              v-model:value="primaryColor"
              :show-alpha="false"
              :modes="['hex']"
              :swatches="['#10b981', '#3b82f6', '#6366f1', '#8b5cf6', '#ec4899', '#f59e0b', '#06b6d4', '#ef4444']"
              size="medium"
            />
          </n-modal>
        </div>
      </header>

      <nav v-if="!isStandaloneCompare" class="mode-container">
        <div class="mode-chips">
          <button
            class="mode-chip"
            :class="{ selected: searchMode === 'clip', 'is-locked': isTaskBusy }"
            :disabled="isTaskBusy"
            @click="!isTaskBusy && (searchMode = 'clip')"
          >
            <ImageIcon :size="15" />
            <span>全图场景检索 (CLIP)</span>
          </button>
          <button
            class="mode-chip"
            :class="{ selected: searchMode === 'face', 'is-locked': isTaskBusy }"
            :disabled="isTaskBusy"
            @click="!isTaskBusy && (searchMode = 'face')"
          >
            <User :size="15" />
            <span>人脸精准匹配 (Face)</span>
          </button>
          <button
            class="mode-chip"
            :class="{ selected: searchMode === 'compare', 'is-locked': isTaskBusy }"
            :disabled="isTaskBusy"
            @click="!isTaskBusy && (searchMode = 'compare')"
          >
            <Users :size="15" />
            <span>人脸 1:1 比对 (Compare)</span>
          </button>
        </div>
      </nav>

      <!-- 未鉴权防呆横幅 -->
      <div v-if="!isStandaloneCompare && !isEmbedded && isCookieChecked && !hasAuth" class="alert-box-banner">
        <div class="alert-box-icon"><AlertTriangle :size="20" /></div>
        <div class="alert-box-content">
          <div class="alert-box-title">
            未检测到 Immich 登录状态且未配置 API Key
          </div>
          <div class="alert-box-sub">
            系统已开启权限物理隔离。请在当前浏览器登录 Immich，或手动配置 API
            Key 限定受权图库。
          </div>
        </div>
        <button
          class="alert-box-btn"
          @click="
            showSettingsModal = true;
            if (keyRecords.length === 0) addKeyRow();
          "
        >
          立即配置
        </button>
      </div>

      <!-- 人脸 1:1 独立比对组件 -->
      <FaceCompare
        v-if="searchMode === 'compare'"
        :api-base="API_BASE"
        :has-auth="hasAuth"
        :active-keys-joined="activeKeysJoined"
        @open-settings="showSettingsModal = true"
        @update:comparing="(val: boolean) => isFaceComparing = val"
      />

      <section
        v-if="searchMode !== 'compare'"
        class="hero-stage"
        :class="{
          dragging: isDragging,
          'has-preview': !!queryPreviewUrl,
          disabled: !hasAuth,
        }"
        @dragover.prevent="if (hasAuth) isDragging = true;"
        @dragleave.prevent="isDragging = false"
        @drop="if (hasAuth) onDrop($event);"
      >
        <div
          v-if="!queryPreviewUrl"
          class="empty-upload-view"
          @click="() => {
            if (isTaskBusy) return;
            triggerWarmup(searchMode);
            hasAuth ? fileInputRef?.click() : (showSettingsModal = true);
          }"
        >
        <div class="drop-illustration">
          <component :is="searchMode === 'face' ? User : ImageIcon" :size="40" />
        </div>

          <div class="drop-title">
            点击选择图片、拖入文件，或在页面按 <span class="shortcut-tip"><kbd>Ctrl + V</kbd> 粘贴</span>
          </div>
          <div class="drop-desc">
            {{
              cookieUser
                ? `已自动识别当前 Immich 账号 [${cookieUser.name}]。支持自由局部选区、多人脸定位与即时过滤。`
                : hasAuth
                  ? `已绑定 ${activeKeys.length} 个授权账号。`
                  : "请先登录 Immich 或配置 API Key 以解除图库检索锁定"
            }}
          </div>
        </div>

        <div v-else class="preview-stage-view">
          <div class="stage-toolbar">
            <div class="stage-info">
              <span class="stage-tag">{{
                searchMode === "face" ? "人脸目标" : "视觉特征"
              }}</span>
              <span class="stage-filename" :title="queryFileName">{{
                queryFileName
              }}</span>
              <span v-if="isSubImageQuery" class="sub-query-badge"
                ><Crop :size="10" /> 局部选区</span
              >
            </div>

            <div class="stage-actions">
              <button
                class="stage-param-mobile-btn"
                @click="showParamModal = true"
                title="调节检索阈值与返回数量"
              >
                <SlidersHorizontal :size="12" />
                <span>参数设置</span>
                <span class="param-summary-pill"
                  >{{ threshold }}% · {{ topK }}项</span
                >
              </button>

              <div class="stage-actions-group">
                <button
                  v-if="!isCroppingMode"
                  class="stage-btn primary"
                  :disabled="isTaskBusy"
                  @click="!isTaskBusy && (isCroppingMode = true)"
                >
                  <Crop :size="12" /> 框选局部
                </button>
                <template v-else>
                  <button class="stage-btn success" :disabled="isTaskBusy" @click="!isTaskBusy && applyRoiCrop()">
                    <Check :size="12" /> 确认
                  </button>
                  <button
                    class="stage-btn"
                    :disabled="isTaskBusy"
                    @click="cancelCrop"
                  >
                    取消
                  </button>
                </template>

                <button
                  v-if="isSubImageQuery"
                  class="stage-btn"
                  :disabled="isTaskBusy"
                  @click="!isTaskBusy && resetToFullImage()"
                >
                  原图
                </button>
                <button
                  class="stage-btn"
                  :disabled="isTaskBusy"
                  @click="() => {
                    if (isTaskBusy) return;
                    triggerWarmup(searchMode);
                    fileInputRef?.click();
                  }"
                >
                  更换
                </button>
                <button class="stage-btn danger" :disabled="isTaskBusy" @click="!isTaskBusy && clearCurrentQuery()">
                  清除
                </button>
              </div>
            </div>
          </div>

          <div class="image-center-wrapper">
            <div
              ref="cropContainerRef"
              class="image-interactive-canvas"
              :class="{ 'crop-active': isCroppingMode }"
              @pointerdown="onCropPointerDown"
              @pointermove="onCropPointerMove"
              @pointerup="onCropPointerUp"
            >
              <img
                ref="heroImgRef"
                :key="queryPreviewUrl"
                :src="queryPreviewUrl"
                alt="检索输入图"
                class="hero-render-img"
                draggable="false"
              />

              <template
                v-if="
                  searchMode === 'face' && !isCroppingMode && !isSubImageQuery
                "
              >
                <div
                  v-for="face in detectedFaces"
                  :key="face.index"
                  class="face-bounding-rect"
                  :class="{ selected: face.index === selectedFaceIndex }"
                  :style="{
                    left: `${face.boundingBox.x1 * 100}%`,
                    top: `${face.boundingBox.y1 * 100}%`,
                    width: `${(face.boundingBox.x2 - face.boundingBox.x1) * 100}%`,
                    height: `${(face.boundingBox.y2 - face.boundingBox.y1) * 100}%`,
                  }"
                  @click.stop="selectFace(face.index)"
                >
                  <div class="face-tag-bubble">
                    #{{ face.index + 1 }} ({{ Math.round(face.score * 100) }}%)
                  </div>
                </div>
              </template>

              <div
                v-if="isCroppingMode && cropStart && cropCurrent"
                class="roi-selection-box"
                :style="cropBoxStyle"
              >
                <div class="roi-tag">局部选区</div>
              </div>
            </div>
          </div>

          <div
            v-if="
              searchMode === 'face' &&
              detectedFaces.length > 1 &&
              !isSubImageQuery
            "
            class="faces-chip-bar"
          >
            <span class="faces-chip-label"
              >检测到 {{ detectedFaces.length }} 张面孔:</span
            >
            <button
              v-for="f in detectedFaces"
              :key="f.index"
              class="face-select-chip"
              :class="{ active: f.index === selectedFaceIndex }"
              @click="selectFace(f.index)"
            >
              <User :size="11" /> #{{ f.index + 1 }} ({{ Math.round(f.score * 100) }}%)
            </button>
          </div>

          <!-- 桌面端控制滑块 -->
          <div class="stage-quick-controls">
            <div class="quick-slider-group">
              <div class="slider-header-row">
                <span class="slider-title">相似度截断:</span>
                <b class="slider-value-text">{{ threshold }}%</b>
                <span class="slider-live-hint">60fps 实时</span>
              </div>
              <n-slider v-model:value="threshold" :min="0" :max="85" :step="5" :marks="thresholdMarks" :format-tooltip="(v: number) => `${v}%`" />
              <div
                class="drag-slider-track"
                @mousedown="onMouseDownSlider($event, 'threshold')"
                @touchstart="onTouchStartSlider($event, 'threshold')"
                @touchmove="onTouchMoveSlider"
                @touchend="onTouchEndSlider"
                @click.prevent.stop
              >
                <div
                  class="drag-slider-fill"
                  :style="{ width: `${getSliderRatio('threshold') * 100}%` }"
                ></div>
                <div
                  class="drag-slider-thumb"
                  :style="{ left: `${getSliderRatio('threshold') * 100}%` }"
                ></div>
              </div>
            </div>

            <div class="quick-slider-group">
              <div class="slider-header-row">
                <span class="slider-title">展示上限:</span>
                <b class="slider-value-text">{{ topK }} 项</b>
              </div>
              <n-slider v-model:value="topK" :min="6" :max="60" :step="6" :marks="topKMarks" :format-tooltip="(v: number) => `${v} 项`" />
              <div
                class="drag-slider-track"
                @mousedown="onMouseDownSlider($event, 'topK')"
                @touchstart="onTouchStartSlider($event, 'topK')"
                @touchmove="onTouchMoveSlider"
                @touchend="onTouchEndSlider"
                @click.prevent.stop
              >
                <div
                  class="drag-slider-fill"
                  :style="{ width: `${getSliderRatio('topK') * 100}%` }"
                ></div>
                <div
                  class="drag-slider-thumb"
                  :style="{ left: `${getSliderRatio('topK') * 100}%` }"
                ></div>
              </div>
            </div>

            <div
              class="quick-slider-group"
              :class="{ disabled: searchMode !== 'face' }"
            >
              <div class="slider-header-row">
                <span class="slider-title">人脸灵敏度:</span>
                <b class="slider-value-text">{{ minScore }}</b>
                <span v-if="searchMode !== 'face'" class="slider-mode-tag"
                  >仅人脸生效</span
                >
              </div>
              <n-slider v-model:value="minScore" :min="0.1" :max="0.7" :step="0.05" :marks="minScoreMarks" :disabled="searchMode !== 'face'" :format-tooltip="(v: number) => `${v}`" />
              <div
                class="drag-slider-track"
                @mousedown="
                  searchMode === 'face' && onMouseDownSlider($event, 'minScore')
                "
                @touchstart="
                  searchMode === 'face' &&
                  onTouchStartSlider($event, 'minScore')
                "
                @touchmove="searchMode === 'face' && onTouchMoveSlider($event)"
                @touchend="onTouchEndSlider"
                @click.prevent.stop
              >
                <div
                  class="drag-slider-fill"
                  :style="{ width: `${getSliderRatio('minScore') * 100}%` }"
                ></div>
                <div
                  class="drag-slider-thumb"
                  :style="{ left: `${getSliderRatio('minScore') * 100}%` }"
                ></div>
              </div>
            </div>
          </div>

          <div class="stage-footer-status">
            <div v-if="isLoading" class="status-badge loading">
              <span class="spinner"></span>
              <span>正在提取图像特征并检索匹配相册...</span>
            </div>
            <div v-else-if="errorMessage" class="status-badge error">
              <AlertTriangle :size="13" />
              <span>{{ errorMessage }}</span>
            </div>
            <div v-else class="status-badge success">
              <Check :size="13" />
              <span>命中 {{ displayedResults.length }} 项结果</span>
              <span class="cache-badge" v-if="isCacheHit"
                ><Zap :size="12" /> 向量缓存命中 ({{ searchCostMs }} ms)</span
              >
              <span class="cost-badge" v-else
                >(耗时 {{ searchCostMs }} ms)</span
              >
            </div>
          </div>
        </div>

        <input
          ref="fileInputRef"
          type="file"
          accept="image/*, application/octet-stream"
          class="hidden-input"
          @change="onFileSelect"
        />
      </section>

      <!-- 搜索结果瀑布流 -->
      <main v-if="searchMode !== 'compare'" class="results-layout">
        <div
          v-if="displayedResults && displayedResults.length > 0"
          class="results-masonry-row"
        >
          <div
            v-for="(col, colIdx) in masonryColumns"
            :key="colIdx"
            class="masonry-col"
          >
            <article
              v-for="item in col"
              :key="item.assetId"
              class="masonry-card"
              @click="activeLightboxItem = item"
            >
              <div
                class="card-media"
                :class="{ 'is-loaded': !!loadedThumbnails[item.assetId] }"
              >
                <img
                  :src="getThumbnailDisplayUrl(item.assetId)"
                  :alt="item.originalFileName"
                  :class="{ 'is-loaded': !!loadedThumbnails[item.assetId] }"
                  loading="lazy"
                  class="natural-thumbnail"
                  @load="onThumbnailLoaded(item.assetId, $event)"
                  @error="onThumbnailError(item.assetId, $event)"
                />

                <a
                  :href="getNativeViewerUrl(item)"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="card-jump-btn"
                  title="在手机浏览器中查看原生大图"
                  @click.stop.prevent="openInExternalBrowser(getNativeViewerUrl(item), $event)"
                >
                  <svg
                    viewBox="0 0 24 24"
                    width="13"
                    height="13"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="2.2"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                  >
                    <path
                      d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"
                    ></path>
                    <polyline points="15 3 21 3 21 9"></polyline>
                    <line x1="10" y1="14" x2="21" y2="3"></line>
                  </svg>
                </a>

                <div
                  class="card-badge"
                  :style="getSimilarityBadgeStyle(item.similarity_pct)"
                >
                  {{ item.similarity_pct }}%
                </div>
              </div>

              <div class="card-body">
                <div class="asset-name" :title="item.originalFileName">
                  {{ item.originalFileName }}
                </div>
                <div class="asset-date">{{ item.date_str }}</div>
              </div>
            </article>
          </div>
        </div>

        <!-- 检索异常/超时专属提示卡片与重试按钮 -->
        <div
          v-else-if="!isLoading && errorMessage"
          class="empty-state-card error-state"
        >
          <div class="empty-icon"><Clock :size="32" /></div>
          <div class="empty-title">请求中断或处理超时</div>
          <div class="empty-subtitle">
            {{ errorMessage }}
          </div>
          <div class="empty-action">
            <button class="adjust-btn retry-btn" @click="triggerSearch()">
              <RotateCw :size="13" /> 立即重新检索
            </button>
          </div>
        </div>

        <!-- 正常无结果提示 -->
        <div
          v-else-if="!isLoading && queryPreviewUrl && !errorMessage"
          class="empty-state-card"
        >
          <div class="empty-icon"><Target :size="32" /></div>
          <div class="empty-title">
            {{ allRawResults.length === 0 ? "未检索到匹配照片" : "当前阈值下无匹配项" }}
          </div>
          <div class="empty-subtitle">
            <template v-if="allRawResults.length === 0">
              图库未检出相似内容（或服务冷启动响应异常），建议清空缓存重试。
            </template>
            <template v-else>
              相似度阈值为 <b>{{ threshold }}%</b>。已将低于此分数的照片完全隐藏。
            </template>
          </div>
          <div class="empty-action" style="display: flex; gap: 8px; justify-content: center; flex-wrap: wrap;">
            <button
              v-if="allRawResults.length > 0 && threshold > 0"
              class="adjust-btn"
              @click="threshold = 0"
            >
              一键重置阈值为 0%
            </button>
            <button class="adjust-btn retry-btn" @click="retryForceFresh()">
              <RotateCw :size="13" /> 清空缓存并重新检索
            </button>
          </div>
        </div>
      </main>

      <!-- 移动端参数弹窗 -->
      <Transition name="modal-fade">
      <div
        v-if="showParamModal"
        class="modal-backdrop"
        @click="showParamModal = false"
      >
        <div class="settings-modal" @click.stop>
          <div class="modal-header">
            <div class="modal-title"><SlidersHorizontal :size="15" /> 检索参数微调</div>
            <button class="modal-close" @click="showParamModal = false">
              <CloseIcon :size="15" />
            </button>
          </div>

          <div class="modal-body">
            <div class="modal-slider-box">
              <div class="slider-header-row">
                <span class="slider-title">相似度截断阈值:</span>
                <b class="slider-value-text">{{ threshold }}%</b>
              </div>
              <n-slider v-model:value="threshold" :min="0" :max="85" :step="5" :marks="thresholdMarks" :format-tooltip="(v: number) => `${v}%`" />
              <div
                class="drag-slider-track"
                @mousedown="onMouseDownSlider($event, 'threshold')"
                @touchstart="onTouchStartSlider($event, 'threshold')"
                @touchmove="onTouchMoveSlider"
                @touchend="onTouchEndSlider"
                @click.prevent.stop
              >
                <div
                  class="drag-slider-fill"
                  :style="{ width: `${getSliderRatio('threshold') * 100}%` }"
                ></div>
                <div
                  class="drag-slider-thumb"
                  :style="{ left: `${getSliderRatio('threshold') * 100}%` }"
                ></div>
              </div>
            </div>

            <div class="modal-slider-box">
              <div class="slider-header-row">
                <span class="slider-title">展示数量上限:</span>
                <b class="slider-value-text">{{ topK }} 项</b>
              </div>
              <n-slider v-model:value="topK" :min="6" :max="60" :step="6" :marks="topKMarks" :format-tooltip="(v: number) => `${v} 项`" />
              <div
                class="drag-slider-track"
                @mousedown="onMouseDownSlider($event, 'topK')"
                @touchstart="onTouchStartSlider($event, 'topK')"
                @touchmove="onTouchMoveSlider"
                @touchend="onTouchEndSlider"
                @click.prevent.stop
              >
                <div
                  class="drag-slider-fill"
                  :style="{ width: `${getSliderRatio('topK') * 100}%` }"
                ></div>
                <div
                  class="drag-slider-thumb"
                  :style="{ left: `${getSliderRatio('topK') * 100}%` }"
                ></div>
              </div>
            </div>

            <div
              class="modal-slider-box"
              :class="{ disabled: searchMode !== 'face' }"
            >
              <div class="slider-header-row">
                <span class="slider-title">人脸检测灵敏度 (minScore):</span>
                <b class="slider-value-text">{{ minScore }}</b>
              </div>
              <n-slider v-model:value="minScore" :min="0.1" :max="0.7" :step="0.05" :marks="minScoreMarks" :disabled="searchMode !== 'face'" :format-tooltip="(v: number) => `${v}`" />
              <div
                class="drag-slider-track"
                @mousedown="
                  searchMode === 'face' && onMouseDownSlider($event, 'minScore')
                "
                @touchstart="
                  searchMode === 'face' &&
                  onTouchStartSlider($event, 'minScore')
                "
                @touchmove="searchMode === 'face' && onTouchMoveSlider($event)"
                @touchend="onTouchEndSlider"
                @click.prevent.stop
              >
                <div
                  class="drag-slider-fill"
                  :style="{ width: `${getSliderRatio('minScore') * 100}%` }"
                ></div>
                <div
                  class="drag-slider-thumb"
                  :style="{ left: `${getSliderRatio('minScore') * 100}%` }"
                ></div>
              </div>
            </div>
          </div>

          <div class="modal-footer">
            <button class="modal-btn-confirm" @click="showParamModal = false">
              完成
            </button>
          </div>
        </div>
      </div>

      <!-- 凭据管理模态框 (展示 Cookie 状态 + 额外 API Key 扩展) -->
      </Transition>
      <Transition name="modal-fade">
      <div
        v-if="!isEmbedded && showSettingsModal"
        class="modal-backdrop"
        @click="showSettingsModal = false"
      >
        <div class="settings-modal" @click.stop>
          <div class="modal-header">
            <div class="modal-title"><KeyRound :size="15" /> 检索授权与凭据管理</div>
            <button class="modal-close" @click="showSettingsModal = false">
              <CloseIcon :size="15" />
            </button>
          </div>

          <div class="modal-body">
            <!-- 1. 当前 Immich 会话状态卡片 -->
            <div
              class="cookie-auth-status"
              :class="{ active: !!cookieUser, inactive: !cookieUser }"
            >
              <div class="cookie-status-icon">
                <ShieldCheck v-if="cookieUser" :size="24" />
                <ShieldAlert v-else :size="24" />
              </div>
              <div class="cookie-status-info">
                <div class="cookie-status-title">
                  {{
                    cookieUser
                      ? "同域登录态已激活 (Session 直通)"
                      : "未检测到 Immich 登录态"
                  }}
                </div>
                <div class="cookie-status-desc">
                  {{
                    cookieUser
                      ? `已识别登录用户：${cookieUser.name} (${cookieUser.email})。已自动绑定当前相册及伴侣共享库。`
                      : "当前在外部环境或尚未登录 Immich。请在下方配置至少一个 API Key 以执行检索。"
                  }}
                </div>
              </div>
            </div>

            <!-- 2. 多 API Key 扩展配置 -->
            <div class="key-mgmt-section">
              <div class="section-title-bar">
                <div class="title-with-badge">
                  <KeyRound :size="13" />
                  <span>跨账号 / 外部 API Key</span>
                  <span class="optional-badge" v-if="cookieUser">选填扩展</span>
                  <span class="required-badge" v-else>未登录必填</span>
                </div>
                <button class="add-key-btn" @click="addKeyRow">
                  + 新增账号
                </button>
              </div>
              <p class="key-mgmt-desc">
                已登录状态下，配置额外的 API Key
                可用于突破单账号限制，实现多成员相册跨库联合匹配。
              </p>

              <div v-if="keyRecords.length === 0" class="empty-keys-tip">
                暂未添加外部 API Key。{{
                  cookieUser
                    ? "当前可直接依靠登录态正常检索。"
                    : "请至少添加一个有效的 API Key。"
                }}
              </div>

              <div v-else class="keys-list">
                <div
                  v-for="(item, idx) in keyRecords"
                  :key="item.id"
                  class="key-row-card"
                >
                  <div class="key-row-main">
                    <n-switch v-model:value="item.enabled" size="small" />
                    <input
                      type="text"
                      v-model="item.label"
                      placeholder="账号备注"
                      class="key-label-input"
                    />
                    <input
                      type="password"
                      v-model="item.key"
                      placeholder="输入 API Key"
                      class="key-val-input"
                    />
                    <button
                      class="key-test-btn"
                      :disabled="item.isTesting"
                      @click="testSingleKey(item)"
                    >
                      {{ item.isTesting ? "验证中" : "测试" }}
                    </button>
                    <button class="key-del-btn" @click="removeKeyRow(idx)">
                      ✕
                    </button>
                  </div>

                  <div
                    v-if="item.userName || item.errorMsg"
                    class="key-status-line"
                  >
                    <span v-if="item.isValid" class="status-badge-mini success">
                      <Check :size="12" /> 鉴权通过: <b>{{ item.userName }}</b>
                    </span>
                    <span
                      v-else-if="item.errorMsg"
                      class="status-badge-mini error"
                    >
                      <CloseIcon :size="12" /> {{ item.errorMsg }}
                    </span>
                  </div>
                </div>
              </div>
            </div>
          </div>

          <div class="modal-footer">
            <button
              class="modal-btn-confirm"
              @click="showSettingsModal = false"
            >
              完成并保存
            </button>
          </div>
        </div>
      </div>

      <!-- 全局系统设置模态框 -->
      </Transition>
      <Transition name="modal-fade">
      <div
        v-if="!isEmbedded && showSystemSettingsModal"
        class="modal-backdrop"
        @click="showSystemSettingsModal = false"
      >
        <div class="settings-modal" @click.stop>
          <div class="modal-header">
            <div class="modal-title"><Settings :size="15" /> 系统全局设置</div>
            <button class="modal-close" @click="showSystemSettingsModal = false">
              <CloseIcon :size="15" />
            </button>
          </div>

          <div class="modal-body">
            <div class="system-setting-card">
              <div class="system-setting-info">
                <div class="system-setting-title">
                  <Trash2 :size="14" />
                  <span>以图搜图结果缓存</span>
                </div>
                <div class="system-setting-desc">
                  清除本地 IndexedDB 与运行内存中存储的搜图特征及匹配清单，下次重新提交图片时将重新向服务端发起计算。
                </div>
              </div>
              <div class="system-setting-action">
                <button
                  class="stage-btn danger"
                  :disabled="isClearingCache"
                  @click="handleClearSearchCache"
                >
                  {{ isClearingCache ? "清理中..." : "清空搜图缓存" }}
                </button>
              </div>
            </div>
            <div v-if="clearCacheNotice" class="system-setting-notice">
              {{ clearCacheNotice }}
            </div>
          </div>

          <div class="modal-footer">
            <button
              class="modal-btn-confirm"
              @click="showSystemSettingsModal = false"
            >
              关闭
            </button>
          </div>
        </div>
      </div>
      </Transition>

      <Transition name="modal-fade">
      <div
        v-if="showShareChoiceModal"
        class="modal-backdrop"
        @click="cancelShareChoice"
      >
        <div class="settings-modal share-choice-modal" @click.stop>
          <div class="modal-header">
            <div class="modal-title">📥 收到相册分享图片</div>
            <button class="modal-close" @click="cancelShareChoice">
              <CloseIcon :size="15" />
            </button>
          </div>
          <div class="modal-body share-choice-body">
            <div v-if="sharedIncomingPreviewUrl" class="share-preview-box">
              <img :src="sharedIncomingPreviewUrl" class="share-preview-thumb" alt="分享图片预览" />
            </div>
            <p class="share-choice-desc">已获取分享的第一张图片，请选择检索类型：</p>
            <div class="share-choice-actions">
              <button class="share-action-card" @click="applyShareChoice('clip')">
                <div class="share-action-icon"><ImageIcon :size="22" /></div>
                <div class="share-action-info">
                  <div class="share-action-title">以图搜图 (CLIP)</div>
                  <div class="share-action-sub">检索构图、色彩与全图视觉语义</div>
                </div>
              </button>
              <button class="share-action-card" @click="applyShareChoice('face')">
                <div class="share-action-icon"><User :size="22" /></div>
                <div class="share-action-info">
                  <div class="share-action-title">搜人脸 (Face)</div>
                  <div class="share-action-sub">提取面部特征并匹配人物相册</div>
                </div>
              </button>
            </div>
          </div>
          <div class="modal-footer">
            <button class="stage-btn" @click="cancelShareChoice">取消</button>
          </div>
        </div>
      </div>
      </Transition>

      <!-- Lightbox 弹窗 -->
      <Transition name="modal-fade" @after-enter="onLightboxAfterEnter">
      <div
        v-if="activeLightboxItem"
        class="lightbox-overlay"
        @click="activeLightboxItem = null"
      >
        <div class="lightbox-modal" @click.stop>
          <div class="lightbox-header">
            <span class="lightbox-title">{{
              activeLightboxItem.originalFileName
            }}</span>
            <button class="lightbox-close" @click="activeLightboxItem = null">
              <CloseIcon :size="17" />
            </button>
          </div>
          <div class="lightbox-body">
            <!-- 底层：已缓存的低清缩略图第一时间铺满展示，带轻微柔化，杜绝弹窗中途尺寸突变 -->
            <img
              :src="activeLightboxThumbSrc"
              class="lightbox-img lightbox-thumb-layer"
              :class="{ 'is-blur': !isHighResReady }"
            />
            <!-- 顶层：高清大图，等资源完全解码且弹窗入场动画停稳后再平滑淡现 -->
            <img
              v-if="activeLightboxHdSrc"
              :src="activeLightboxHdSrc"
              class="lightbox-img lightbox-hd-layer"
              :class="{ 'is-visible': isHighResReady && !isLightboxOpening }"
              @load="isHighResReady = true"
            />
            <div v-if="isHighResLoading || (!isHighResReady && !isLightboxOpening)" class="hires-loading-badge">
              <span class="spinner-mini"></span>
              <span>高清加载中...</span>
            </div>
          </div>
          <div class="lightbox-footer">
            <div class="lightbox-meta">
              <div>拍摄时间: {{ activeLightboxItem.date_str }}</div>
              <div
                style="
                  font-weight: 600;
                  color: var(--md-primary);
                  margin-top: 2px;
                "
              >
                匹配相似度: {{ activeLightboxItem.similarity_pct }}%
              </div>
            </div>
            <div class="lightbox-actions">
              <a
                :href="getNativeViewerUrl(activeLightboxItem)"
                target="_blank"
                rel="noopener noreferrer"
                class="action-btn"
                @click.prevent="openInExternalBrowser(getNativeViewerUrl(activeLightboxItem), $event)"
              >
                <ExternalLink :size="13" />
                <span>原生大图预览</span>
              </a>
              <a
                :href="getTimelineUrl(activeLightboxItem)"
                target="_blank"
                rel="noopener noreferrer"
                class="action-btn timeline-btn"
                @click.prevent="openInExternalBrowser(getTimelineUrl(activeLightboxItem), $event)"
              >
                <Clock :size="13" />
                <span>时间线位置</span>
              </a>
            </div>
          </div>
        </div>
      </div>
      </Transition>
    </div>
  </div>
  </n-config-provider>
</template>

<style>
:root {
  --md-primary: #10b981;
  --md-on-primary: #ffffff;
  --md-primary-container: #d1fae5;
  --md-on-primary-container: #064e3b;
  --md-surface: #fdfcff;
  --md-surface-dim: #ded8e1;
  --md-surface-container: #ffffff;
  --md-surface-container-high: #f1f5f9;
  --md-on-surface: #1a1c1e;
  --md-on-surface-variant: #43474e;
  --md-outline: #73777f;
  --md-outline-variant: #c3c7cf;
  --md-error-container: #ffdad6;
  --md-on-error-container: #410002;
  --md-shadow: rgba(0, 0, 0, 0.08);
}

html.dark {
  --md-primary: #10b981;
  --md-on-primary: #042f2e;
  --md-primary-container: #004a76;
  --md-on-primary-container: #cee5ff;
  --md-surface: #0a0b0d;
  --md-surface-dim: #0a0b0d;
  --md-surface-container: #12141a;
  --md-surface-container-high: #1a1d26;
  --md-on-surface: #e2e2e5;
  --md-on-surface-variant: #c3c7cf;
  --md-outline: #8d9199;
  --md-outline-variant: #43474e;
  --md-error-container: #93000a;
  --md-on-error-container: #ffdad6;
  --md-shadow: rgba(0, 0, 0, 0.35);
}

* {
  box-sizing: border-box;
  margin: 0;
  padding: 0;
}
:root {
  color-scheme: light dark;
}
html.dark {
  color-scheme: dark;
}
html {
  background-color: var(--md-surface);
  min-height: 100%;
}
body {
  background-color: var(--md-surface);
  color: var(--md-on-surface);
  min-height: 100vh;
  min-height: 100dvh;
  overflow-x: hidden;
  font-family:
    "Roboto",
    -apple-system,
    BlinkMacSystemFont,
    "Segoe UI",
    sans-serif;
}
html,
body,
#app {
  border: none !important;
  border-width: 0 !important;
  outline: none !important;
  box-shadow: none !important;
}
#app {
  background-color: var(--md-surface);
  min-height: 100vh;
  min-height: 100dvh;
}
</style>

<style scoped>
.app-wrapper {
  padding: 16px 12px 40px;
}
.app-wrapper.is-embedded {
  --md-surface: #ffffff !important;
  --md-surface-dim: #f8fafc !important;
  --md-surface-container: #f1f5f9 !important;
  --md-surface-container-high: #e2e8f0 !important;
  --md-on-surface: #0f172a !important;
  --md-on-surface-variant: #64748b !important;
  --md-outline: #cbd5e1 !important;
  --md-outline-variant: #e2e8f0 !important;
  --md-primary: #4250af !important;
  --md-primary-container: rgba(66, 80, 175, 0.12) !important;
  --md-on-primary-container: #4250af !important;
  --md-shadow: rgba(0, 0, 0, 0.06) !important;
  padding: 12px 16px 36px !important;
  background-color: var(--md-surface) !important;
  min-height: 100vh;
}
.app-wrapper.is-embedded.is-dark {
  --md-surface: #141517 !important;
  --md-surface-dim: #0e0f11 !important;
  --md-surface-container: #1e2023 !important;
  --md-surface-container-high: #2b2d32 !important;
  --md-on-surface: #f1f5f9 !important;
  --md-on-surface-variant: #94a3b8 !important;
  --md-outline: #475569 !important;
  --md-outline-variant: rgba(255, 255, 255, 0.08) !important;
  --md-primary: #3b82f6 !important;
  --md-primary-container: rgba(59, 130, 246, 0.2) !important;
  --md-on-primary-container: #93c5fd !important;
  --md-shadow: rgba(0, 0, 0, 0.4) !important;
  background-color: var(--md-surface) !important;
}
.embedded-nav-bar {
  display: flex;
  justify-content: space-between;
  align-items: stretch;
  margin-bottom: 14px;
  padding-bottom: 10px;
  border-bottom: 1px solid var(--md-outline-variant);
}
.embedded-title-group {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--md-primary);
  font-size: 13px;
  font-weight: 600;
  min-width: 0;
  white-space: nowrap;
}
.embedded-title-group span {
  white-space: nowrap;
}
.embedded-badge {
  font-size: 10px;
  padding: 2px 7px;
  border-radius: 9999px;
  background: var(--md-primary-container);
  color: var(--md-primary);
  font-weight: 500;
  letter-spacing: 0.2px;
  border: 1px solid var(--md-outline-variant);
  white-space: nowrap;
  flex-shrink: 0;
}
.embedded-title-col {
  display: flex;
  flex-direction: column;
  justify-content: center;
  align-items: flex-start;
  gap: 2px;
  min-width: 0;
}
.embedded-title-text {
  font-size: 13px;
  font-weight: 600;
  line-height: 1.2;
  white-space: nowrap;
}
.badge-engine.embedded-badge {
  display: inline-flex !important;
  align-self: flex-start;
  font-size: 9px !important;
  line-height: 1.2;
  padding: 1px 6px;
  border-radius: 4px;
  white-space: nowrap;
}
.windmill-icon {
  display: block;
  flex-shrink: 0;
  transition: transform 0.6s cubic-bezier(0.34, 1.56, 0.64, 1);
}
.brand-avatar:hover .windmill-icon,
.embedded-title-group:hover .windmill-icon {
  transform: rotate(180deg);
}
.embedded-close-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  background: var(--md-surface-container);
  border: 1px solid var(--md-outline-variant);
  color: var(--md-on-surface);
  padding: 5px 12px;
  border-radius: 20px;
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
  flex-shrink: 0;
}
.embedded-close-btn:hover {
  background: var(--md-surface-container-high);
  border-color: var(--md-primary);
  color: var(--md-primary);
}
.container {
  max-width: 1280px;
  margin: 0 auto;
}

.app-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  margin-bottom: 16px;
}

.brand-cluster {
  display: flex;
  align-items: center;
  gap: 10px;
}

.brand-avatar {
  background: var(--md-surface-container-high);
  width: 40px;
  height: 40px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 10px;
  border: 1px solid var(--md-outline-variant);
  flex-shrink: 0;
}
.avatar-symbol {
  font-size: 22px;
  line-height: 1;
}

.brand-text {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.title-row {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0;
}

.brand-title {
  font-size: 19px;
  font-weight: 700;
  color: var(--md-primary);
  line-height: 1.1;
  margin: 0 !important;
}

.badge-engine {
  font-size: 9px;
  font-weight: 600;
  background: var(--md-primary-container);
  color: var(--md-on-primary-container);
  padding: 1px 5px;
  border-radius: 6px;
}
.brand-subtitle {
  font-size: 11px;
  color: var(--md-on-surface-variant);
  line-height: 1.1;
  margin: 0;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.settings-badge-btn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  background: var(--md-surface-container);
  border: 1px solid var(--md-outline-variant);
  color: var(--md-on-surface);
  padding: 5px 10px;
  border-radius: 18px;
  font-size: 11px;
  cursor: pointer;
  min-height: 34px;
}
.settings-badge-btn.warning {
  border-color: #f59e0b;
  background: rgba(245, 158, 11, 0.1);
}
.active-count-tag {
  font-size: 9px;
  background: var(--md-primary-container);
  color: var(--md-on-primary-container);
  padding: 1px 5px;
  border-radius: 6px;
  font-weight: 600;
}
.active-count-tag.red {
  background: rgba(239, 68, 68, 0.2);
  color: #ef4444;
}

.color-picker-box {
  width: 34px;
  height: 34px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}
.color-picker-box :deep(.n-color-picker-trigger) {
  border-radius: 10px;
  border: 1px solid var(--md-outline-variant);
  background: var(--md-surface-container);
  height: 34px;
  width: 34px;
  padding: 4px;
}
.color-picker-box :deep(.n-color-picker-trigger) {
  font-size: 0 !important;
  color: transparent !important;
  line-height: 0 !important;
}
.color-picker-box :deep(.n-color-picker-trigger *) {
  font-size: 0 !important;
  color: transparent !important;
}
.color-picker-box :deep(.n-color-picker-trigger__value) {
  display: none !important;
  visibility: hidden !important;
  opacity: 0 !important;
  width: 0 !important;
  height: 0 !important;
  overflow: hidden !important;
  pointer-events: none !important;
}
.color-picker-box :deep(.n-color-picker-trigger__fill) {
  border-radius: 6px;
  width: 100% !important;
  height: 100% !important;
}
.drag-slider-track {
  display: none !important;
}
.theme-segmented {
  display: inline-flex;
  background: var(--md-surface-container);
  padding: 2px;
  border-radius: 18px;
  border: 1px solid var(--md-outline-variant);
}
.seg-item {
  background: none;
  border: none;
  padding: 5px 8px;
  border-radius: 14px;
  color: var(--md-on-surface-variant);
  font-size: 11px;
  cursor: pointer;
}
.seg-item.active {
  background: var(--md-primary-container);
  color: var(--md-on-primary-container);
  font-weight: 600;
}

.mode-container {
  display: flex;
  justify-content: center;
  margin-bottom: 14px;
}
.mode-chips {
  display: flex;
  gap: 8px;
  max-width: 100%;
  overflow-x: auto;
  scrollbar-width: none;
  -webkit-overflow-scrolling: touch;
  padding: 2px 0;
}
.mode-chips::-webkit-scrollbar {
  display: none;
}
.mode-chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 6px 14px;
  border-radius: 20px;
  background: var(--md-surface-container);
  border: 1px solid var(--md-outline-variant);
  color: var(--md-on-surface-variant);
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  min-height: 34px;
  white-space: nowrap;
  flex-shrink: 0;
}
.mode-chip.selected {
  background: var(--md-primary);
  color: var(--md-on-primary);
  border-color: var(--md-primary);
  font-weight: 600;
}
.mode-chip.is-locked {
  opacity: 0.55;
  cursor: not-allowed;
  pointer-events: none;
}
.stage-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.alert-box-banner {
  background: rgba(245, 158, 11, 0.12);
  border: 1px solid rgba(245, 158, 11, 0.4);
  border-radius: 12px;
  padding: 8px 14px;
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 14px;
}
.alert-box-icon {
  font-size: 20px;
}
.alert-box-content {
  flex: 1;
}
.alert-box-title {
  font-size: 12px;
  font-weight: 700;
  color: #b45309;
}
.alert-box-sub {
  font-size: 10px;
  color: var(--md-on-surface-variant);
  margin-top: 1px;
}
.alert-box-btn {
  background: #f59e0b;
  color: #000;
  border: none;
  padding: 4px 10px;
  border-radius: 10px;
  font-size: 11px;
  font-weight: 600;
  cursor: pointer;
}

.hero-stage {
  background: var(--md-surface-container);
  border: 2px dashed var(--md-outline-variant);
  border-radius: 20px;
  box-shadow: var(--md-shadow);
  transition: all 0.2s ease;
  overflow: hidden;
}
.hero-stage.has-preview {
  border: 1px solid var(--md-outline-variant);
  padding: 10px 12px;
}
.hero-stage.disabled {
  opacity: 0.7;
}
.hero-stage:hover:not(.disabled),
.hero-stage.dragging {
  border-color: var(--md-primary);
  background: var(--md-surface-container-high);
}

.empty-upload-view {
  padding: 40px 14px;
  text-align: center;
  cursor: pointer;
}
.drop-illustration {
  font-size: 36px;
  margin-bottom: 6px;
}
.drop-title {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 4px;
}
kbd {
  background: var(--md-surface-container-high);
  padding: 2px 4px;
  border-radius: 4px;
  border: 1px solid var(--md-outline-variant);
  font-size: 10px;
  white-space: nowrap;
}
.shortcut-tip {
  white-space: nowrap;
}
.drop-desc {
  font-size: 11px;
  color: var(--md-on-surface-variant);
}

@keyframes stageEntrance {
  from { opacity: 0; transform: scale(0.985); }
  to { opacity: 1; transform: scale(1); }
}
.preview-stage-view {
  display: flex;
  flex-direction: column;
  gap: 8px;
  animation: stageEntrance 0.28s cubic-bezier(0.16, 1, 0.3, 1);
}

.stage-toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  padding-bottom: 6px;
  border-bottom: 1px solid var(--md-outline-variant);
}

.stage-info {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.stage-tag {
  background: var(--md-primary-container);
  color: var(--md-on-primary-container);
  font-size: 9px;
  font-weight: 700;
  padding: 1px 5px;
  border-radius: 4px;
}
.sub-query-badge {
  background: #f59e0b;
  color: #000;
  font-size: 9px;
  font-weight: 700;
  padding: 1px 5px;
  border-radius: 4px;
}
.stage-filename {
  font-size: 11px;
  font-weight: 600;
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.stage-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.stage-param-mobile-btn {
  display: none;
  align-items: center;
  gap: 5px;
  background: var(--md-surface-container-high);
  border: 1px solid var(--md-outline-variant);
  color: var(--md-on-surface);
  padding: 4px 8px;
  border-radius: 14px;
  font-size: 11px;
  cursor: pointer;
  min-height: 28px;
  font-weight: 500;
}

.param-summary-pill {
  font-size: 9px;
  background: var(--md-primary-container);
  color: var(--md-on-primary-container);
  padding: 1px 5px;
  border-radius: 6px;
  font-weight: 600;
}

.stage-actions-group {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-wrap: wrap;
}

.stage-btn {
  background: var(--md-surface);
  border: 1px solid var(--md-outline-variant);
  color: var(--md-on-surface);
  padding: 3px 8px;
  border-radius: 10px;
  font-size: 11px;
  cursor: pointer;
  min-height: 28px;
}
.stage-btn.primary {
  background: var(--md-primary-container);
  color: var(--md-on-primary-container);
  font-weight: 600;
}
.stage-btn.success {
  background: #22c55e;
  color: #fff;
  border-color: #22c55e;
  font-weight: 600;
}
.stage-btn.danger {
  color: #ef4444;
  border-color: rgba(239, 68, 68, 0.4);
}

.image-center-wrapper {
  display: flex;
  justify-content: center;
  align-items: center;
  background: rgba(0, 0, 0, 0.25);
  border-radius: 10px;
  padding: 6px;
}
.image-interactive-canvas {
  position: relative;
  display: inline-block;
  line-height: 0;
  max-width: 100%;
  user-select: none;
  touch-action: pan-y;
}
.image-interactive-canvas.crop-active {
  touch-action: none;
  cursor: crosshair;
}

@keyframes imageFadeIn {
  from { opacity: 0; }
  to { opacity: 1; }
}
.hero-render-img {
  display: block;
  animation: imageFadeIn 0.35s ease-out;
  max-height: 220px;
  max-width: 100%;
  width: auto;
  height: auto;
  border-radius: 6px;
  object-fit: contain;
}

.roi-selection-box {
  position: absolute;
  border: 2px dashed #f59e0b;
  background: rgba(245, 158, 11, 0.25);
  pointer-events: none;
  z-index: 20;
}
.roi-tag {
  position: absolute;
  top: -16px;
  left: 0;
  background: #f59e0b;
  color: #000;
  font-size: 8px;
  font-weight: bold;
  padding: 1px 3px;
  border-radius: 3px;
}

.face-bounding-rect {
  position: absolute;
  border: 2px solid #38bdf8;
  background: rgba(56, 189, 248, 0.2);
  cursor: pointer;
  transition: all 0.15s ease;
  z-index: 10;
}
.face-bounding-rect.selected {
  border: 2px solid #22c55e;
  background: rgba(34, 197, 94, 0.35);
  box-shadow: 0 0 6px rgba(34, 197, 94, 0.9);
}
.face-tag-bubble {
  position: absolute;
  top: -15px;
  left: -2px;
  background: #000;
  color: #fff;
  font-size: 8px;
  font-weight: bold;
  padding: 1px 3px;
  border-radius: 3px;
}

.faces-chip-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px;
  background: var(--md-surface);
  padding: 4px 8px;
  border-radius: 8px;
  border: 1px solid var(--md-outline-variant);
}
.faces-chip-label {
  font-size: 10px;
  color: var(--md-on-surface-variant);
}
.face-select-chip {
  background: var(--md-surface-container);
  border: 1px solid var(--md-outline-variant);
  color: var(--md-on-surface);
  padding: 2px 6px;
  border-radius: 8px;
  font-size: 10px;
  cursor: pointer;
}
.face-select-chip.active {
  background: var(--md-primary-container);
  color: var(--md-on-primary-container);
  font-weight: 600;
}

.stage-quick-controls {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 8px;
  background: var(--md-surface);
  padding: 8px 10px;
  border-radius: 10px;
  border: 1px solid var(--md-outline-variant);
}
.quick-slider-group {
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.quick-slider-group.disabled {
  opacity: 0.45;
  pointer-events: none;
}
.slider-header-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 11px;
}
.slider-title {
  color: var(--md-on-surface-variant);
}
.slider-value-text {
  font-size: 11px;
  color: var(--md-on-surface);
}
.slider-live-hint {
  font-size: 9px;
  color: var(--md-primary);
  font-weight: 600;
}
.slider-mode-tag {
  font-size: 9px;
  color: var(--md-outline);
}

.modal-slider-box {
  display: flex;
  flex-direction: column;
  gap: 6px;
  background: var(--md-surface);
  padding: 10px 12px;
  border-radius: 10px;
  border: 1px solid var(--md-outline-variant);
}
.modal-slider-box.disabled {
  opacity: 0.4;
  pointer-events: none;
}

.drag-slider-track {
  position: relative;
  height: 20px;
  display: flex;
  align-items: center;
  cursor: pointer;
  user-select: none;
  touch-action: pan-y;
}
.drag-slider-track::before {
  content: "";
  position: absolute;
  left: 0;
  right: 0;
  height: 5px;
  background: var(--md-surface-container-high);
  border-radius: 3px;
}
.drag-slider-fill {
  position: absolute;
  left: 0;
  height: 5px;
  background: var(--md-primary);
  border-radius: 3px;
  pointer-events: none;
}
.drag-slider-thumb {
  position: absolute;
  width: 15px;
  height: 15px;
  border-radius: 50%;
  background: var(--md-primary);
  border: 2px solid var(--md-surface);
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.3);
  transform: translate(-50%, 0);
  pointer-events: none;
}

.stage-footer-status {
  display: flex;
  justify-content: flex-start;
}
.status-badge {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 3px 10px;
  border-radius: 10px;
  font-size: 11px;
}
.status-badge.loading {
  background: var(--md-primary-container);
  color: var(--md-on-primary-container);
}
.status-badge.success {
  background: var(--md-surface-container-high);
  color: var(--md-primary);
  font-weight: 600;
}
.status-badge.error {
  background: var(--md-error-container);
  color: var(--md-on-error-container);
}
.cache-badge {
  color: #16a34a;
  font-weight: bold;
  margin-left: 2px;
}
.cost-badge {
  color: var(--md-on-surface-variant);
  font-size: 10px;
  margin-left: 2px;
}
.spinner {
  width: 10px;
  height: 10px;
  border: 2px solid currentColor;
  border-top-color: transparent;
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}
@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

.hidden-input {
  display: none;
}

.results-layout {
  margin-top: 18px;
}
.results-masonry-row {
  display: flex;
  gap: 12px;
  align-items: flex-start;
  width: 100%;
}
.masonry-col {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-width: 0;
}

.masonry-card {
  width: 100%;
  background: var(--md-surface-container);
  border-radius: 14px;
  backdrop-filter: blur(10px);
  overflow: hidden;
  border: 1px solid var(--md-outline-variant);
  display: flex;
  flex-direction: column;
  cursor: pointer;
  animation: cardEntrance 0.35s cubic-bezier(0.16, 1, 0.3, 1) backwards;
  transition:
    transform 0.2s,
    box-shadow 0.2s;
}
@keyframes cardEntrance {
  from {
    opacity: 0;
  }
  to {
    opacity: 1;
  }
}
.masonry-card:hover {
  transform: translateY(-4px);
  box-shadow: 0 12px 28px -4px var(--md-shadow), 0 0 0 1px var(--md-primary);
}
.card-media {
  position: relative;
  width: 100%;
  min-height: 180px;
  background: var(--md-surface-container-high);
  overflow: hidden;
}
.card-media::before {
  content: "";
  position: absolute;
  inset: -20px;
  background: linear-gradient(
    90deg,
    transparent 0%,
    rgba(255, 255, 255, 0.04) 35%,
    rgba(255, 255, 255, 0.18) 50%,
    rgba(255, 255, 255, 0.04) 65%,
    transparent 100%
  );
  transform: translateX(-100%) skewX(-15deg);
  animation: glassShimmer 2.2s infinite cubic-bezier(0.25, 1, 0.5, 1);
  pointer-events: none;
  z-index: 1;
}
html:not(.dark) .card-media::before {
  background: linear-gradient(
    90deg,
    transparent 0%,
    rgba(255, 255, 255, 0.35) 35%,
    rgba(255, 255, 255, 0.75) 50%,
    rgba(255, 255, 255, 0.35) 65%,
    transparent 100%
  );
}
@keyframes glassShimmer {
  0% { transform: translateX(-100%) skewX(-15deg); }
  100% { transform: translateX(200%) skewX(-15deg); }
}
.natural-thumbnail {
  position: relative;
  z-index: 2;
  opacity: 0;
  transition: opacity 0.35s ease-out;
  width: 100%;
  height: auto;
  display: block;
}

.natural-thumbnail.is-loaded {
  opacity: 1;
}
.card-media.is-loaded {
  min-height: unset;
  background: transparent;
}
.card-media.is-loaded::before {
  display: none;
  content: none;
  animation: none;
}
.card-jump-btn {
  position: absolute;
  top: 6px;
  left: 6px;
  width: 22px;
  height: 22px;
  border-radius: 5px;
  background: rgba(0, 0, 0, 0.45);
  backdrop-filter: blur(4px);
  color: #ffffff;
  display: flex;
  align-items: center;
  justify-content: center;
  text-decoration: none;
  transition:
    background-color 0.15s,
    transform 0.15s;
  z-index: 3;
}
.card-jump-btn:hover {
  background: rgba(0, 0, 0, 0.8);
  transform: scale(1.1);
}

.card-badge {
  position: absolute;
  top: 6px;
  right: 6px;
  z-index: 3;
  backdrop-filter: blur(4px);
  color: #fff;
  font-size: 10px;
  font-weight: 700;
  padding: 1px 5px;
  border-radius: 5px;
  border: 1px solid rgba(255, 255, 255, 0.18);
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.3);
}

.card-body {
  padding: 5px 8px;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.asset-name {
  font-size: 11px;
  font-weight: 600;
  line-height: 1.25;
  margin: 0 !important;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.asset-date {
  font-size: 10px;
  color: var(--md-on-surface-variant);
  line-height: 1.2;
  margin: 0 !important;
}

.empty-state-card {
  text-align: center;
  padding: 30px 14px;
  background: var(--md-surface-container);
  border-radius: 14px;
  border: 1px solid var(--md-outline-variant);
  max-width: 440px;
  margin: 0 auto;
}
.empty-icon {
  font-size: 28px;
  margin-bottom: 4px;
}
.empty-title {
  font-size: 13px;
  font-weight: 600;
}
.empty-subtitle {
  font-size: 10px;
  color: var(--md-on-surface-variant);
  margin: 3px 0 10px;
}
.system-setting-card {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  background: var(--md-surface);
  border: 1px solid var(--md-outline-variant);
  border-radius: 12px;
  padding: 12px 14px;
}
.system-setting-info {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.system-setting-title {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  font-weight: 700;
  color: var(--md-on-surface);
}
.system-setting-desc {
  font-size: 11px;
  color: var(--md-on-surface-variant);
  line-height: 1.4;
}
.system-setting-notice {
  font-size: 11px;
  color: var(--md-primary);
  font-weight: 600;
  text-align: right;
  padding-right: 4px;
}
.adjust-btn {
  background: var(--md-primary);
  color: var(--md-on-primary);
  border: none;
  padding: 5px 12px;
  border-radius: 12px;
  font-size: 10px;
  font-weight: 600;
  cursor: pointer;
}

/* 模态框与会话状态卡片 */
.modal-fade-enter-active,
.modal-fade-leave-active {
  transition: opacity 0.35s cubic-bezier(0.16, 1, 0.3, 1);
}
.modal-fade-enter-active .settings-modal,
.modal-fade-enter-active .lightbox-modal {
  transition: transform 0.35s cubic-bezier(0.16, 1, 0.3, 1), opacity 0.35s cubic-bezier(0.16, 1, 0.3, 1);
}
.modal-fade-leave-active {
  transition: opacity 0.28s ease-in-out;
}
.modal-fade-leave-active .settings-modal,
.modal-fade-leave-active .lightbox-modal {
  transition: transform 0.28s cubic-bezier(0.4, 0, 1, 1), opacity 0.28s ease-in-out;
}
.modal-fade-enter-from,
.modal-fade-leave-to {
  opacity: 0;
}
.modal-fade-enter-from .settings-modal,
.modal-fade-enter-from .lightbox-modal,
.modal-fade-leave-to .settings-modal,
.modal-fade-leave-to .lightbox-modal {
  transform: scale(0.94) translateY(12px);
  opacity: 0;
}
@keyframes modalScaleIn {
  from { opacity: 0; transform: scale(0.93) translateY(10px); }
  to { opacity: 1; transform: scale(1) translateY(0); }
}
.modal-backdrop {
  position: fixed;
  top: 0;
  left: 0;
  width: 100vw;
  height: 100vh;
  background: rgba(0, 0, 0, 0.65);
  backdrop-filter: blur(4px);
  z-index: 999;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 14px;
}
.settings-modal {
  background: var(--md-surface-container);
  border: 1px solid var(--md-outline-variant);
  border-radius: 16px;
  max-width: 600px;
  width: 100%;
  max-height: 85vh;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  box-shadow: 0 8px 32px var(--md-shadow);
}
.modal-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px 16px;
  border-bottom: 1px solid var(--md-outline-variant);
}
.modal-title {
  font-size: 13px;
  font-weight: 700;
  color: var(--md-on-surface);
}
.modal-close {
  background: none;
  border: none;
  font-size: 15px;
  color: var(--md-on-surface);
  cursor: pointer;
}
.modal-body {
  padding: 14px 16px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.cookie-auth-status {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 14px;
  border-radius: 10px;
  border: 1px solid var(--md-outline-variant);
}
.cookie-auth-status.active {
  background: rgba(34, 197, 94, 0.12);
  border-color: rgba(34, 197, 94, 0.4);
}
.cookie-auth-status.inactive {
  background: var(--md-surface);
}
.cookie-status-icon {
  font-size: 24px;
}
.cookie-status-info {
  flex: 1;
}
.cookie-status-title {
  font-size: 12px;
  font-weight: 700;
  color: var(--md-on-surface);
}
.cookie-status-desc {
  font-size: 10px;
  color: var(--md-on-surface-variant);
  margin-top: 1px;
}

.section-title-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 4px;
}
.title-with-badge {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 11px;
  font-weight: 700;
}
.optional-badge {
  font-size: 8px;
  font-weight: bold;
  color: #15803d;
  background: rgba(34, 197, 94, 0.15);
  padding: 1px 4px;
  border-radius: 4px;
}
.required-badge {
  font-size: 8px;
  font-weight: bold;
  color: #dc2626;
  background: rgba(220, 38, 38, 0.1);
  padding: 1px 4px;
  border-radius: 4px;
}
.add-key-btn {
  background: var(--md-primary);
  color: var(--md-on-primary);
  border: none;
  padding: 2px 7px;
  border-radius: 8px;
  font-size: 10px;
  font-weight: 600;
  cursor: pointer;
}
.key-mgmt-desc {
  font-size: 10px;
  color: var(--md-on-surface-variant);
  margin-bottom: 6px;
}
.empty-keys-tip {
  font-size: 10px;
  color: var(--md-outline);
  padding: 10px;
  text-align: center;
  border: 1px dashed var(--md-outline-variant);
  border-radius: 6px;
}

.keys-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.key-row-card {
  background: var(--md-surface);
  border: 1px solid var(--md-outline-variant);
  border-radius: 8px;
  padding: 6px 8px;
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.key-row-main {
  display: flex;
  align-items: center;
  gap: 5px;
  flex-wrap: wrap;
}
.key-enable-chk {
  width: 14px;
  height: 14px;
  cursor: pointer;
}
.key-label-input {
  width: 90px;
  padding: 3px 5px;
  background: var(--md-surface-container);
  border: 1px solid var(--md-outline-variant);
  color: var(--md-on-surface);
  border-radius: 4px;
  font-size: 10px;
}
.key-val-input {
  flex: 1;
  min-width: 140px;
  padding: 3px 5px;
  background: var(--md-surface-container);
  border: 1px solid var(--md-outline-variant);
  color: var(--md-on-surface);
  border-radius: 4px;
  font-size: 10px;
}
.key-test-btn {
  background: var(--md-surface-container-high);
  border: 1px solid var(--md-outline-variant);
  color: var(--md-on-surface);
  padding: 3px 6px;
  border-radius: 4px;
  font-size: 10px;
  cursor: pointer;
}
.key-del-btn {
  background: none;
  border: none;
  font-size: 12px;
  color: #ef4444;
  cursor: pointer;
}
.key-status-line {
  font-size: 9px;
  padding-left: 18px;
}
.status-badge-mini {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 1px 4px;
  border-radius: 4px;
}
.status-badge-mini.success {
  background: rgba(34, 197, 94, 0.15);
  color: #15803d;
}
.status-badge-mini.error {
  background: rgba(239, 68, 68, 0.15);
  color: #b91c1c;
}

.modal-footer {
  padding: 8px 14px;
  border-top: 1px solid var(--md-outline-variant);
  display: flex;
  justify-content: flex-end;
}
.modal-btn-confirm {
  background: var(--md-primary);
  color: var(--md-on-primary);
  border: none;
  padding: 5px 14px;
  border-radius: 12px;
  font-size: 11px;
  font-weight: 600;
  cursor: pointer;
}

.lightbox-overlay {
  position: fixed;
  top: 0;
  left: 0;
  width: 100vw;
  height: 100vh;
  background: rgba(0, 0, 0, 0.88);
  backdrop-filter: blur(12px);
  z-index: 1000;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 12px;
}
.lightbox-modal {
  position: relative;
  background: rgba(0, 0, 0, 0.88);
  border: none;
  border-radius: 16px;
  box-shadow: 0 25px 60px -10px rgba(0, 0, 0, 0.8), 0 0 0 1px rgba(255, 255, 255, 0.14);
  max-width: 94vw;
  width: min(94vw, 1280px);
  max-height: 90vh;
  height: min(88vh, 860px);
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
}
.lightbox-header {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  z-index: 10;
  display: flex;
  justify-content: space-between;
  align-items: center;
  pointer-events: none;
  padding: 14px 18px 36px;
  background: linear-gradient(180deg, rgba(0, 0, 0, 0.78) 0%, rgba(0, 0, 0, 0.25) 60%, transparent 100%);
  border-bottom: none;
}
.lightbox-title {
  font-size: 13px;
  font-weight: 600;
  color: #ffffff;
  text-shadow: 0 1px 4px rgba(0, 0, 0, 0.9), 0 2px 10px rgba(0, 0, 0, 0.7);
  max-width: 75%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  pointer-events: auto;
}
.lightbox-close {
  background: rgba(0, 0, 0, 0.45);
  border: 1px solid rgba(255, 255, 255, 0.22);
  width: 32px;
  height: 32px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  backdrop-filter: blur(8px);
  box-shadow: 0 2px 10px rgba(0, 0, 0, 0.5);
  filter: drop-shadow(0 1px 3px rgba(0, 0, 0, 0.8));
  transition: all 0.2s ease;
  pointer-events: auto;
  color: #ffffff;
  cursor: pointer;
}
.lightbox-close:hover {
  background: rgba(255, 255, 255, 0.25);
  transform: scale(1.08);
  border-color: #ffffff;
}
.lightbox-body {
  position: relative;
  padding: 0;
  background: transparent;
  width: 100%;
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
}
.lightbox-img {
  width: 100%;
  height: 100%;
  max-width: 100%;
  max-height: 100%;
  object-fit: contain;
  border-radius: 12px;
  display: block;
  user-select: none;
}
.lightbox-thumb-layer {
  transition: filter 0.35s ease-out;
}
.lightbox-thumb-layer.is-blur {
  filter: blur(8px) brightness(0.95);
  transform: scale(1.02);
}
.lightbox-hd-layer {
  position: absolute;
  inset: 0;
  margin: auto;
  opacity: 0;
  transition: opacity 0.4s cubic-bezier(0.16, 1, 0.3, 1);
  z-index: 2;
}
.lightbox-hd-layer.is-visible {
  opacity: 1;
}
.hires-loading-badge {
  position: absolute;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: rgba(0, 0, 0, 0.65);
  border: 1px solid rgba(255, 255, 255, 0.18);
  backdrop-filter: blur(10px);
  color: #ffffff;
  padding: 6px 14px;
  border-radius: 20px;
  font-size: 11px;
  font-weight: 500;
  pointer-events: none;
  z-index: 5;
}
.spinner-mini {
  width: 11px;
  height: 11px;
  border: 2px solid rgba(255, 255, 255, 0.3);
  border-top-color: #ffffff;
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}
.lightbox-meta {
  pointer-events: auto;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.lightbox-footer {
  position: absolute;
  bottom: 0;
  left: 0;
  right: 0;
  z-index: 10;
  display: flex;
  justify-content: space-between;
  align-items: flex-end;
  pointer-events: none;
  gap: 12px;
  padding: 40px 18px 14px;
  background: linear-gradient(0deg, rgba(0, 0, 0, 0.85) 0%, rgba(0, 0, 0, 0.35) 60%, transparent 100%);
  border-top: none;
  font-size: 11px;
  color: #ffffff;
  text-shadow: 0 1px 4px rgba(0, 0, 0, 0.9), 0 2px 8px rgba(0, 0, 0, 0.7);
}
.lightbox-footer > div {
  pointer-events: auto;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.lightbox-actions {
  pointer-events: auto;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.action-btn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  background: rgba(0, 0, 0, 0.5);
  backdrop-filter: blur(10px);
  border: 1px solid rgba(255, 255, 255, 0.24);
  color: #ffffff;
  padding: 6px 14px;
  border-radius: 20px;
  font-size: 11px;
  font-weight: 500;
  text-shadow: 0 1px 3px rgba(0, 0, 0, 0.9);
  box-shadow: 0 4px 14px rgba(0, 0, 0, 0.5);
  transition: all 0.2s cubic-bezier(0.16, 1, 0.3, 1);
}
.action-btn:hover {
  background: rgba(255, 255, 255, 0.22);
  border-color: rgba(255, 255, 255, 0.5);
  transform: translateY(-2px);
  box-shadow: 0 6px 20px rgba(0, 0, 0, 0.6);
  color: #ffffff;
}
.action-btn.timeline-btn {
  background: rgba(30, 41, 59, 0.6);
  border-color: rgba(255, 255, 255, 0.2);
  color: #ffffff;
}
.action-btn.timeline-btn:hover {
  background: rgba(59, 130, 246, 0.4);
  border-color: #3b82f6;
  color: #ffffff;
}

@media (max-width: 640px) {
  .app-wrapper {
    padding: 12px 8px 30px;
  }
  .app-wrapper.is-embedded {
    padding: 8px 8px 24px !important;
  }
  .embedded-badge {
    display: none !important;
  }
  .embedded-title-group {
    font-size: 12px;
    gap: 6px;
  }
  .embedded-close-btn {
    padding: 4px 8px;
    font-size: 11px;
  }
  .mode-chips {
    justify-content: flex-start;
  }
  .mode-chip {
    padding: 5px 10px;
    font-size: 11px;
    min-height: 30px;
  }
  .drop-title {
    font-size: 12px;
    line-height: 1.4;
  }
  .drop-desc {
    font-size: 10px;
    line-height: 1.4;
  }
  .app-header {
    flex-direction: column;
    align-items: stretch;
    gap: 8px;
  }
  .brand-cluster {
    justify-content: flex-start;
  }
  .header-actions {
    justify-content: space-between;
  }
  .brand-title {
    font-size: 17px;
    margin: 0 !important;
  }
  .brand-subtitle {
    font-size: 10px;
  }
  .stage-toolbar {
    flex-direction: column;
    align-items: stretch;
    gap: 6px;
  }
  .stage-actions {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .stage-param-mobile-btn {
    display: inline-flex !important;
    margin-right: auto;
  }
  .stage-actions-group {
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .stage-quick-controls {
    display: none !important;
  }
  .hero-render-img {
    max-height: 180px;
  }
  .results-masonry-row {
    gap: 8px;
  }
  .masonry-col {
    gap: 8px;
  }
}

.share-choice-modal {
  max-width: 400px;
}
.share-choice-body {
  align-items: center;
  text-align: center;
  gap: 12px;
}
.share-preview-box {
  width: 100%;
  max-height: 160px;
  display: flex;
  justify-content: center;
  background: rgba(0, 0, 0, 0.2);
  border-radius: 10px;
  padding: 6px;
}
.share-preview-thumb {
  max-height: 148px;
  max-width: 100%;
  border-radius: 6px;
  object-fit: contain;
}
.share-choice-desc {
  font-size: 12px;
  color: var(--md-on-surface-variant);
}
.share-choice-actions {
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 100%;
}
.share-action-card {
  display: flex;
  align-items: center;
  gap: 12px;
  background: var(--md-surface);
  border: 1px solid var(--md-outline-variant);
  border-radius: 12px;
  padding: 10px 14px;
  cursor: pointer;
  transition: all 0.2s ease;
  text-align: left;
}
.share-action-card:hover {
  border-color: var(--md-primary);
  background: var(--md-primary-container);
  transform: translateY(-1px);
}
.share-action-icon {
  color: var(--md-primary);
  display: flex;
  align-items: center;
  justify-content: center;
}
.share-action-title {
  font-size: 13px;
  font-weight: 700;
  color: var(--md-on-surface);
}
.share-action-sub {
  font-size: 10px;
  color: var(--md-on-surface-variant);
  margin-top: 1px;
}
</style>
