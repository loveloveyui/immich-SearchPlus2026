<script setup lang="ts">
import { ref, watch, computed, onMounted, onUnmounted } from "vue";
import { NSlider } from "naive-ui";
import { User, Camera, AlertTriangle, RotateCw } from "lucide-vue-next";
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

interface CompareResponse {
  similarity: number;
  similarity_pct: number;
  verdict: string;
  verdict_level: "strong" | "likely" | "uncertain" | "unlikely";
  detected_faces_a: DetectedFace[];
  selected_face_a: number;
  detected_faces_b: DetectedFace[];
  selected_face_b: number;
  cost_ms: number;
  detail?: string;
}

const props = defineProps<{
  apiBase: string;
  hasAuth: boolean;
  activeKeysJoined: string;
}>();

function triggerWarmup() {
  const query = props.activeKeysJoined ? `&api_key=${encodeURIComponent(props.activeKeysJoined)}` : "";
  fetch(`${props.apiBase}/api/warmup?mode=face${query}`, { credentials: "include" }).catch(() => {});
}

const emit = defineEmits<{
  (e: "open-settings"): void;
  (e: "update:comparing", val: boolean): void;
}>();

// 激活卡片选择状态：默认激活卡片 A
const activeCard = ref<"A" | "B">("A");

const fileA = ref<File | null>(null);
const fileB = ref<File | null>(null);
const previewA = ref<string>("");
const previewB = ref<string>("");

const facesA = ref<DetectedFace[]>([]);
const facesB = ref<DetectedFace[]>([]);
const selectedA = ref<number>(-1);
const selectedB = ref<number>(-1);

// 人脸灵敏度参数
const minScore = ref<number>(0.25);
const compareMarks = { 0.1: "0.1", 0.25: "0.25", 0.5: "0.5", 0.7: "0.7" };
const isComparing = ref<boolean>(false);
const compareResult = ref<CompareResponse | null>(null);
const errorMsg = ref<string>("");

const inputRefA = ref<HTMLInputElement | null>(null);
const inputRefB = ref<HTMLInputElement | null>(null);

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

async function preprocessFaceClient(file: File): Promise<File> {
  try {
    const bitmap = await createImageBitmap(file);
    const origW = bitmap.width;
    const origH = bitmap.height;
    const maxDim = 640;
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

async function handleFileSelect(rawFile: File, side: "A" | "B") {
  const file = await convertHeicToJpeg(rawFile);
  if (!file.type.startsWith("image/")) {
    errorMsg.value = "请上传图片格式文件 (JPEG/PNG/WebP/GIF)";
    return;
  }
  errorMsg.value = "";
  const readyFile = await preprocessFaceClient(file);
  if (side === "A") {
    fileA.value = readyFile;
    previewA.value = URL.createObjectURL(file);
    selectedA.value = -1;
    facesA.value = [];
  } else {
    fileB.value = readyFile;
    previewB.value = URL.createObjectURL(readyFile);
    selectedB.value = -1;
    facesB.value = [];
  }

  if (fileA.value && fileB.value) {
    runComparison();
  }
}

// 监听剪贴板粘贴事件
function onWindowPaste(e: ClipboardEvent) {
  if (isComparing.value) return;
  const items = e.clipboardData?.items;
  if (!items) return;
  for (const item of items) {
    if (item.type.startsWith("image/")) {
      const blob = item.getAsFile();
      if (blob) {
        const targetSide = activeCard.value;
        handleFileSelect(blob, targetSide);

        // 智能流转：若对面卡片尚未上传图片，自动激活对侧，便于直接再次粘贴
        if (targetSide === "A" && !fileB.value) {
          activeCard.value = "B";
        } else if (targetSide === "B" && !fileA.value) {
          activeCard.value = "A";
        }
      }
      break;
    }
  }
}

async function runComparison(overrideA?: number, overrideB?: number) {
  if (!fileA.value || !fileB.value) return;
  if (!props.hasAuth) {
    errorMsg.value = "请先配置 API Key 或保持 Immich 登录状态";
    emit("open-settings");
    return;
  }

  errorMsg.value = "";
  isComparing.value = true;
  emit("update:comparing", true);

  const targetA = overrideA !== undefined ? overrideA : selectedA.value;
  const targetB = overrideB !== undefined ? overrideB : selectedB.value;

  const formData = new FormData();
  formData.append("file_a", fileA.value);
  formData.append("file_b", fileB.value);
  formData.append("face_index_a", targetA.toString());
  formData.append("face_index_b", targetB.toString());
  formData.append("min_score", minScore.value.toString());
  if (props.activeKeysJoined) {
    formData.append("api_keys", props.activeKeysJoined);
  }

  const headers: Record<string, string> = {};
  if (props.activeKeysJoined) {
    headers["X-Immich-Api-Key"] = props.activeKeysJoined;
  }

  try {
    const res = await fetch(`${props.apiBase}/api/face-compare`, {
      method: "POST",
      headers,
      body: formData,
      credentials: "include",
    });

    const data: CompareResponse = await res.json();
    if (!res.ok) {
      throw new Error(data.detail || `比对请求失败 [${res.status}]`);
    }

    compareResult.value = data;
    facesA.value = data.detected_faces_a || [];
    facesB.value = data.detected_faces_b || [];
    selectedA.value = data.selected_face_a;
    selectedB.value = data.selected_face_b;
  } catch (err: unknown) {
    errorMsg.value = err instanceof Error ? err.message : "比对发生异常";
    compareResult.value = null;
  } finally {
    isComparing.value = false;
    emit("update:comparing", false);
  }
}

// 阈值变动自动重新比对
watch(minScore, () => {
  if (fileA.value && fileB.value) {
    runComparison();
  }
});

function selectFaceA(idx: number) {
  if (selectedA.value === idx || isComparing.value) return;
  selectedA.value = idx;
  runComparison(idx, selectedB.value);
}

function selectFaceB(idx: number) {
  if (selectedB.value === idx || isComparing.value) return;
  selectedB.value = idx;
  runComparison(selectedA.value, idx);
}

function getVerdictColor(level?: string) {
  switch (level) {
    case "strong":
      return "#22c55e";
    case "likely":
      return "#38bdf8";
    case "uncertain":
      return "#f59e0b";
    case "unlikely":
    default:
      return "#ef4444";
  }
}

// ================= minScore 触摸/拖拽滑块防误触控制 =================
const minScoreRatio = computed(() => {
  return Math.max(0, Math.min(1, (minScore.value - 0.1) / (0.7 - 0.1)));
});

function updateMinScoreByClientX(clientX: number, trackLeft: number, trackWidth: number) {
  const ratio = Math.max(0, Math.min(1, (clientX - trackLeft) / trackWidth));
  let raw = 0.1 + ratio * (0.7 - 0.1);
  raw = Math.round(raw / 0.05) * 0.05;
  minScore.value = Number(raw.toFixed(2));
}

let touchStartX = 0;
let touchStartY = 0;
let trackRect: DOMRect | null = null;
let isHorizLocked = false;
let isVertDiscarded = false;

function onTouchStartSlider(e: TouchEvent) {
  if (e.touches.length !== 1) return;
  const touch = e.touches[0];
  const el = e.currentTarget as HTMLElement;
  trackRect = el.getBoundingClientRect();
  touchStartX = touch.clientX;
  touchStartY = touch.clientY;
  isHorizLocked = false;
  isVertDiscarded = false;
}

function onTouchMoveSlider(e: TouchEvent) {
  if (!trackRect || isVertDiscarded || e.touches.length !== 1) return;
  const touch = e.touches[0];
  const dx = touch.clientX - touchStartX;
  const dy = touch.clientY - touchStartY;

  if (!isHorizLocked) {
    if (Math.abs(dy) > Math.abs(dx) && Math.abs(dy) > 5) {
      isVertDiscarded = true;
      return;
    }
    if (Math.abs(dx) > Math.abs(dy) && Math.abs(dx) > 8) {
      isHorizLocked = true;
    } else {
      return;
    }
  }

  if (e.cancelable) e.preventDefault();
  updateMinScoreByClientX(touch.clientX, trackRect.left, trackRect.width);
}

function onTouchEndSlider() {
  trackRect = null;
}

function onMouseDownSlider(e: MouseEvent) {
  const el = e.currentTarget as HTMLElement;
  const rect = el.getBoundingClientRect();
  const startX = e.clientX;
  let isDragging = false;

  const onMouseMove = (ev: MouseEvent) => {
    if (Math.abs(ev.clientX - startX) > 6) isDragging = true;
    if (isDragging) {
      updateMinScoreByClientX(ev.clientX, rect.left, rect.width);
    }
  };

  const onMouseUp = () => {
    window.removeEventListener("mousemove", onMouseMove);
    window.removeEventListener("mouseup", onMouseUp);
  };

  window.addEventListener("mousemove", onMouseMove);
  window.addEventListener("mouseup", onMouseUp);
}

onMounted(() => {
  window.addEventListener("paste", onWindowPaste);
});

onUnmounted(() => {
  window.removeEventListener("paste", onWindowPaste);
});
</script>

<template>
  <div class="compare-container">
    <!-- 双图选择与状态卡片 -->
    <div class="compare-grid">
      <!-- 样本 A -->
      <div
        class="compare-card"
        :class="{ 'is-active': activeCard === 'A' }"
        @click="activeCard = 'A'"
      >
        <div class="card-header">
          <div class="card-title-group">
            <span class="card-title"><User :size="14" /> 人像照片 A</span>
            <span v-if="activeCard === 'A'" class="active-badge active">
              🟢 已激活 (按 Ctrl+V 粘贴)
            </span>
            <span v-else class="active-badge">点击激活</span>
          </div>
          <button
            v-if="previewA"
            class="change-btn"
            :disabled="isComparing"
            @click.stop="() => { if (isComparing) return; triggerWarmup(); inputRefA?.click(); }"
          >
            更换
          </button>
        </div>

        <div
          class="upload-drop-zone"
          :class="{ 'has-img': !!previewA, 'is-busy': isComparing }"
          @click="() => {
            if (isComparing) return;
            triggerWarmup();
            if (!previewA) inputRefA?.click();
          }"
        >
          <div v-if="!previewA" class="zone-placeholder">
            <div class="placeholder-icon"><Camera :size="32" /></div>
            <div class="placeholder-text">点击上传或在此处粘贴图片 A</div>
          </div>

          <div v-else class="preview-wrapper">
            <img :src="previewA" class="compare-img" alt="人像A" />
            <div
              v-for="f in facesA"
              :key="f.index"
              class="face-box"
              :class="{ active: f.index === selectedA }"
              :style="{
                left: `${f.boundingBox.x1 * 100}%`,
                top: `${f.boundingBox.y1 * 100}%`,
                width: `${(f.boundingBox.x2 - f.boundingBox.x1) * 100}%`,
                height: `${(f.boundingBox.y2 - f.boundingBox.y1) * 100}%`,
              }"
              @click.stop="selectFaceA(f.index)"
            >
              <span class="box-tag">#{{ f.index + 1 }}</span>
            </div>
          </div>
        </div>

        <div v-if="facesA.length > 1" class="multi-faces-selector">
          <span class="selector-title">选定对比面孔:</span>
          <button
            v-for="f in facesA"
            :key="f.index"
            class="face-chip"
            :class="{ active: f.index === selectedA }"
            @click.stop="selectFaceA(f.index)"
          >
            #{{ f.index + 1 }} ({{ Math.round(f.score * 100) }}%)
          </button>
        </div>
        <input
          ref="inputRefA"
          type="file"
          accept="image/*, application/octet-stream"
          class="hidden-file"
          @change="
            ($event.target as HTMLInputElement).files?.[0] &&
              handleFileSelect(($event.target as HTMLInputElement).files![0], 'A')
          "
        />
      </div>

      <!-- 样本 B -->
      <div
        class="compare-card"
        :class="{ 'is-active': activeCard === 'B' }"
        @click="activeCard = 'B'"
      >
        <div class="card-header">
          <div class="card-title-group">
            <span class="card-title"><User :size="14" /> 人像照片 B</span>
            <span v-if="activeCard === 'B'" class="active-badge active">
              🟢 已激活 (按 Ctrl+V 粘贴)
            </span>
            <span v-else class="active-badge">点击激活</span>
          </div>
          <button
            v-if="previewB"
            class="change-btn"
            :disabled="isComparing"
            @click.stop="() => { if (isComparing) return; triggerWarmup(); inputRefB?.click(); }"
          >
            更换
          </button>
        </div>

        <div
          class="upload-drop-zone"
          :class="{ 'has-img': !!previewB, 'is-busy': isComparing }"
          @click="() => {
            if (isComparing) return;
            triggerWarmup();
            if (!previewB) inputRefB?.click();
          }"
        >
          <div v-if="!previewB" class="zone-placeholder">
            <div class="placeholder-icon"><Camera :size="32" /></div>
            <div class="placeholder-text">点击上传或在此处粘贴图片 B</div>
          </div>

          <div v-else class="preview-wrapper">
            <img :src="previewB" class="compare-img" alt="人像B" />
            <div
              v-for="f in facesB"
              :key="f.index"
              class="face-box"
              :class="{ active: f.index === selectedB }"
              :style="{
                left: `${f.boundingBox.x1 * 100}%`,
                top: `${f.boundingBox.y1 * 100}%`,
                width: `${(f.boundingBox.x2 - f.boundingBox.x1) * 100}%`,
                height: `${(f.boundingBox.y2 - f.boundingBox.y1) * 100}%`,
              }"
              @click.stop="selectFaceB(f.index)"
            >
              <span class="box-tag">#{{ f.index + 1 }}</span>
            </div>
          </div>
        </div>

        <div v-if="facesB.length > 1" class="multi-faces-selector">
          <span class="selector-title">选定对比面孔:</span>
          <button
            v-for="f in facesB"
            :key="f.index"
            class="face-chip"
            :class="{ active: f.index === selectedB }"
            @click.stop="selectFaceB(f.index)"
          >
            #{{ f.index + 1 }} ({{ Math.round(f.score * 100) }}%)
          </button>
        </div>
        <input
          ref="inputRefB"
          type="file"
          accept="image/*, application/octet-stream"
          class="hidden-file"
          @change="
            ($event.target as HTMLInputElement).files?.[0] &&
              handleFileSelect(($event.target as HTMLInputElement).files![0], 'B')
          "
        />
      </div>
    </div>

    <!-- 人脸检测阈值控制台 -->
    <div class="compare-param-bar">
      <div class="slider-header-row">
        <div class="slider-title-group">
          <span class="slider-title">人脸检测灵敏度 (minScore):</span>
          <b class="slider-val">{{ minScore }}</b>
        </div>
        <span class="slider-hint">检不出面部或侧脸时调低 (默认 0.25)</span>
      </div>
      <n-slider v-model:value="minScore" :min="0.1" :max="0.7" :step="0.05" :marks="compareMarks" :format-tooltip="(v: number) => `${v}`" />
      <div v-if="false"
        class="drag-slider-track"
        @mousedown="onMouseDownSlider"
        @touchstart="onTouchStartSlider"
        @touchmove="onTouchMoveSlider"
        @touchend="onTouchEndSlider"
        @click.prevent.stop
      >
        <div
          class="drag-slider-fill"
          :style="{ width: `${minScoreRatio * 100}%` }"
        ></div>
        <div
          class="drag-slider-thumb"
          :style="{ left: `${minScoreRatio * 100}%` }"
        ></div>
      </div>
    </div>

    <!-- 状态反馈与错误提示 (带重试按钮) -->
    <div v-if="errorMsg" class="compare-status-bar error">
      <AlertTriangle :size="14" />
      <span>{{ errorMsg }}</span>
      <button class="retry-inline-btn" @click="runComparison()">
        <RotateCw :size="12" /> 重试比对
      </button>
    </div>
    <div v-else-if="isComparing" class="compare-status-bar loading">
      <span class="spinner"></span>
      <span>AntelopeV2 特征模型正在提取人脸高维表征并比对...</span>
    </div>

    <!-- 比对结果面板 -->
    <div v-if="compareResult" class="result-board">
      <div class="score-circle-wrapper">
        <div
          class="score-number"
          :style="{ color: getVerdictColor(compareResult.verdict_level) }"
        >
          {{ compareResult.similarity_pct }}%
        </div>
        <div class="score-label">余弦相似度</div>
      </div>

      <div class="verdict-summary">
        <div
          class="verdict-pill"
          :style="{
            backgroundColor: getVerdictColor(compareResult.verdict_level),
          }"
        >
          {{ compareResult.verdict }}
        </div>
        <p class="verdict-notes">
          基于 AntelopeV2 深度人脸特征点比对 · 推理总耗时:
          {{ compareResult.cost_ms }} ms
        </p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.compare-container {
  display: flex;
  flex-direction: column;
  gap: 14px;
  width: 100%;
}
.compare-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 14px;
}
.compare-card {
  background: var(--md-surface-container);
  border: 2px solid var(--md-outline-variant);
  border-radius: 14px;
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  cursor: pointer;
  transition: all 0.2s ease;
}
.compare-card.is-active {
  border-color: var(--md-primary);
  background: var(--md-surface-container-high);
  box-shadow: 0 0 0 1px var(--md-primary), 0 8px 24px -4px var(--md-shadow);
}
.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.card-title-group {
  display: flex;
  align-items: center;
  gap: 8px;
}
.card-title {
  font-size: 13px;
  font-weight: 700;
}
.active-badge {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 6px;
  background: var(--md-surface);
  color: var(--md-on-surface-variant);
  border: 1px solid var(--md-outline-variant);
}
.active-badge.active {
  background: var(--md-primary-container);
  color: var(--md-on-primary-container);
  font-weight: 600;
  border-color: var(--md-primary);
}
.change-btn {
  background: var(--md-surface);
  border: 1px solid var(--md-outline-variant);
  color: var(--md-on-surface);
  font-size: 10px;
  padding: 2px 8px;
  border-radius: 8px;
  cursor: pointer;
}
.change-btn:disabled,
.upload-drop-zone.is-busy {
  opacity: 0.55;
  cursor: not-allowed;
  pointer-events: none;
}
.upload-drop-zone {
  border: 2px dashed var(--md-outline);
  border-radius: 10px;
  min-height: 220px;
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  overflow: hidden;
  background: var(--md-surface);
}
.upload-drop-zone.has-img {
  border-style: solid;
  border-color: var(--md-outline-variant);
  cursor: default;
}
.zone-placeholder {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
  color: var(--md-on-surface-variant);
}
.placeholder-icon {
  font-size: 32px;
}
.placeholder-text {
  font-size: 11px;
}
.preview-wrapper {
  position: relative;
  display: inline-block;
  max-width: 100%;
  line-height: 0;
}
.compare-img {
  max-height: 280px;
  max-width: 100%;
  width: auto;
  height: auto;
  object-fit: contain;
  display: block;
}
.face-box {
  position: absolute;
  border: 2px solid #38bdf8;
  background: rgba(56, 189, 248, 0.2);
  cursor: pointer;
  transition: all 0.15s ease;
}
.face-box.active {
  border-color: #22c55e;
  background: rgba(34, 197, 94, 0.35);
  box-shadow: 0 0 6px rgba(34, 197, 94, 0.9);
}
.box-tag {
  position: absolute;
  top: -14px;
  left: 0;
  background: #000;
  color: #fff;
  font-size: 8px;
  padding: 1px 3px;
  border-radius: 2px;
}
.multi-faces-selector {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-wrap: wrap;
}
.selector-title {
  font-size: 10px;
  color: var(--md-on-surface-variant);
}
.face-chip {
  background: var(--md-surface);
  border: 1px solid var(--md-outline-variant);
  color: var(--md-on-surface);
  font-size: 9px;
  padding: 2px 6px;
  border-radius: 8px;
  cursor: pointer;
}
.face-chip.active {
  background: var(--md-primary-container);
  color: var(--md-on-primary-container);
  font-weight: bold;
}
.hidden-file {
  display: none;
}

/* 人脸检测阈值控制条 */
.compare-param-bar {
  display: flex;
  flex-direction: column;
  gap: 5px;
  background: var(--md-surface-container);
  border: 1px solid var(--md-outline-variant);
  border-radius: 12px;
  padding: 10px 14px;
}
.slider-header-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 11px;
}
.slider-title-group {
  display: flex;
  align-items: center;
  gap: 6px;
}
.slider-title {
  color: var(--md-on-surface-variant);
}
.slider-val {
  color: var(--md-on-surface);
  font-size: 12px;
}
.slider-hint {
  font-size: 10px;
  color: var(--md-outline);
}

.drag-slider-track {
  display: none !important;
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

.compare-status-bar {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 8px 14px;
  border-radius: 10px;
  font-size: 11px;
}
.compare-status-bar.loading {
  background: var(--md-primary-container);
  color: var(--md-on-primary-container);
}
.compare-status-bar.error {
  background: var(--md-error-container);
  color: var(--md-on-error-container);
}
@keyframes resultBoardPop {
  from { opacity: 0; transform: translateY(12px) scale(0.98); }
  to { opacity: 1; transform: translateY(0) scale(1); }
}
.result-board {
  background: var(--md-surface-container);
  border: 1px solid var(--md-outline-variant);
  border-radius: 14px;
  padding: 16px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 32px;
  animation: resultBoardPop 0.3s cubic-bezier(0.16, 1, 0.3, 1);
}
.score-circle-wrapper {
  text-align: center;
}
.score-number {
  font-size: 36px;
  font-weight: 800;
  line-height: 1;
}
.score-label {
  font-size: 10px;
  color: var(--md-on-surface-variant);
  margin-top: 4px;
}
.verdict-summary {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.verdict-pill {
  color: #fff;
  font-size: 14px;
  font-weight: 700;
  padding: 4px 14px;
  border-radius: 16px;
  width: fit-content;
}
.verdict-notes {
  font-size: 10px;
  color: var(--md-on-surface-variant);
}

@media (max-width: 640px) {
  .compare-grid {
    grid-template-columns: 1fr;
  }
  .result-board {
    flex-direction: column;
    gap: 12px;
    text-align: center;
  }
  .verdict-pill {
    margin: 0 auto;
  }
}

.retry-inline-btn {
  background: var(--md-surface);
  border: 1px solid var(--md-outline-variant);
  color: var(--md-on-surface);
  font-size: 11px;
  font-weight: 600;
  padding: 3px 8px;
  border-radius: 6px;
  cursor: pointer;
  margin-left: 8px;
}
.retry-inline-btn:hover {
  background: var(--md-surface-container-high);
}
</style>