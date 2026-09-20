import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

const now = new Date();
const pad = (n: number) => n.toString().padStart(2, "0");
const buildTime = `${now.getFullYear()}.${pad(now.getMonth() + 1)}.${pad(now.getDate())} ${pad(now.getHours())}:${pad(now.getMinutes())}`;

export default defineConfig({
  plugins: [vue()],
  base: "./", // 关键：使用相对路径适配子路径部署
  build: {
    outDir: "../backend/dist", // 直接将构建产物输出至 Go 后端源码树
    emptyOutDir: true,
  },
  define: {
    __BUILD_TIME__: JSON.stringify(buildTime),
  },
});
