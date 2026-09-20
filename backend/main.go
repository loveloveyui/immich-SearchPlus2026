package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"log"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "golang.org/x/image/webp"
)

//go:embed all:dist
var distEmbedFS embed.FS

type Config struct {
	MLPredictURL    string
	ImmichServerURL string
	ImmichAPIURL    string
	CLIPModelName   string
	FaceModelName   string
	DBHost          string
	DBPort          string
	DBName          string
	DBUser          string
	DBPassword      string
}

type UserIdentity struct {
	UserID    string `json:"id"`
	UserName  string `json:"name"`
	UserEmail string `json:"email"`
}

type CachedInference struct {
	Embedding     []float32
	DetectedFaces []FaceItem
	SelectedFace  int
	CreatedAt     time.Time
}

var (
	lastClipWarmup time.Time
	lastFaceWarmup time.Time
	warmupLock     sync.Mutex
	mlSemaphore    = make(chan struct{}, 1)
	cfg            Config
	dbPool         *pgxpool.Pool
	userKeyCache   sync.Map // 凭据内存缓存: key/token -> UserIdentity
	inferenceCache sync.Map // hashKey -> CachedInference
)

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func initConfig() {
	cfg = Config{
		MLPredictURL:    getEnv("IMMICH_ML_URL", "http://immich-machine-learning:3003/predict"),
		ImmichServerURL: strings.TrimRight(getEnv("IMMICH_SERVER_URL", "http://immich-server:2283"), "/"),
		CLIPModelName:   getEnv("IMMICH_CLIP_MODEL", "nllb-clip-large-siglip__v1"),
		FaceModelName:   getEnv("IMMICH_FACE_MODEL", "antelopev2"),
		DBHost:          getEnv("IMMICH_DB_HOST", "database"),
		DBPort:          getEnv("IMMICH_DB_PORT", "5432"),
		DBName:          getEnv("IMMICH_DB_NAME", "immich"),
		DBUser:          getEnv("IMMICH_DB_USER", "postgres"),
		DBPassword:      os.Getenv("IMMICH_DB_PASSWORD"),
	}

	rawAPIURL := strings.TrimRight(os.Getenv("IMMICH_API_URL"), "/")
	if rawAPIURL != "" {
		cfg.ImmichAPIURL = strings.TrimSuffix(rawAPIURL, "/api")
	} else {
		cfg.ImmichAPIURL = cfg.ImmichServerURL
	}
}

func maskKey(k string) string {
	if len(k) <= 8 {
		return "***"
	}
	return k[:4] + "..." + k[len(k)-4:]
}

// 向上游 Immich 校验 Cookie / Session 身份
func resolveUserFromCookie(req *http.Request) (UserIdentity, error) {
	cookieHeader := req.Header.Get("Cookie")
	authHeader := req.Header.Get("Authorization")
	if cookieHeader == "" && authHeader == "" {
		return UserIdentity{}, fmt.Errorf("未检测到 Cookie 或 Bearer 授权头")
	}

	cacheKey := "sess:" + cookieHeader + "|" + authHeader
	if cached, ok := userKeyCache.Load(cacheKey); ok {
		return cached.(UserIdentity), nil
	}

	client := &http.Client{Timeout: 5 * time.Second}
	immichReq, err := http.NewRequest("GET", cfg.ImmichAPIURL+"/api/users/me", nil)
	if err != nil {
		return UserIdentity{}, err
	}
	if cookieHeader != "" {
		immichReq.Header.Set("Cookie", cookieHeader)
	}
	if authHeader != "" {
		immichReq.Header.Set("Authorization", authHeader)
	}

	resp, err := client.Do(immichReq)
	if err != nil {
		return UserIdentity{}, fmt.Errorf("无法连接 Immich 鉴权接口: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return UserIdentity{}, fmt.Errorf("Session 已失效或未登录 (HTTP %d)", resp.StatusCode)
	}

	var user UserIdentity
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return UserIdentity{}, err
	}
	if user.UserName == "" {
		user.UserName = user.UserEmail
	}

	userKeyCache.Store(cacheKey, user)
	return user, nil
}

// 向上游 Immich 校验 API Key 身份
func resolveUser(apiKey string) (UserIdentity, error) {
	trimmed := strings.TrimSpace(apiKey)
	if trimmed == "" {
		return UserIdentity{}, fmt.Errorf("API Key 为空")
	}
	if cached, ok := userKeyCache.Load(trimmed); ok {
		return cached.(UserIdentity), nil
	}

	req, err := http.NewRequest("GET", cfg.ImmichAPIURL+"/api/users/me", nil)
	if err != nil {
		return UserIdentity{}, err
	}
	req.Header.Set("x-api-key", trimmed)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return UserIdentity{}, fmt.Errorf("无法连接 Immich 服务: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return UserIdentity{}, fmt.Errorf("API Key 无效 (HTTP %d)", resp.StatusCode)
	}

	var user UserIdentity
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return UserIdentity{}, err
	}
	if user.UserName == "" {
		user.UserName = user.UserEmail
	}

	userKeyCache.Store(trimmed, user)
	return user, nil
}

func validateMultipleKeys(rawKeys []string) ([]UserIdentity, []string, error) {
	var validUsers []UserIdentity
	var validUserIDs []string
	var errDetails []string

	for _, k := range rawKeys {
		trimmed := strings.TrimSpace(k)
		if trimmed == "" {
			continue
		}
		u, err := resolveUser(trimmed)
		if err != nil {
			errDetails = append(errDetails, fmt.Sprintf("[%s]: %v", maskKey(trimmed), err))
		} else {
			validUsers = append(validUsers, u)
			validUserIDs = append(validUserIDs, u.UserID)
		}
	}

	if len(validUsers) == 0 {
		return nil, nil, fmt.Errorf("提供的 API Key 均无效 (%s)", strings.Join(errDetails, "; "))
	}
	return validUsers, validUserIDs, nil
}

func getPartnerOwnerIDs(ctx context.Context, userIDs []string) []string {
	if len(userIDs) == 0 {
		return nil
	}
	rows, err := dbPool.Query(ctx, `
		SELECT "sharedById"::text 
		FROM public.partners 
		WHERE "sharedWithId"::text = ANY($1)
	`, userIDs)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var pids []string
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

func initDB() {
	connStr := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		cfg.DBUser, cfg.DBPassword, cfg.DBHost, cfg.DBPort, cfg.DBName)

	poolConfig, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		log.Fatalf("数据库配置错误: %v", err)
	}
	poolConfig.MaxConns = 10
	poolConfig.MinConns = 2
	poolConfig.MaxConnLifetime = 1 * time.Hour
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.HealthCheckPeriod = 1 * time.Minute

	dbPool, err = pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		log.Printf("[!] 警告: 数据库初始化连接失败: %v", err)
	} else {
		log.Printf("[✓] PostgreSQL 连接池就绪: %s@%s:%s/%s", cfg.DBUser, cfg.DBHost, cfg.DBPort, cfg.DBName)
		go func() {
			warmCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			t0 := time.Now()
			tables := []string{`"smart_search"`, `"face_search"`, `"asset"`, `"album_asset"`, `"album_user"`}
			for _, tbl := range tables {
				_, _ = dbPool.Exec(warmCtx, fmt.Sprintf(`SELECT 1 FROM public.%s LIMIT 1;`, tbl))
			}
			log.Printf("[✓] 数据库核心表与向量索引预热完成 (耗时: %v)", time.Since(t0))
		}()
	}

	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		for range ticker.C {
			now := time.Now()
			inferenceCache.Range(func(key, value interface{}) bool {
				if item, ok := value.(CachedInference); ok {
					if now.Sub(item.CreatedAt) > 2*time.Hour {
						inferenceCache.Delete(key)
					}
				}
				return true
			})
		}
	}()
}

type ProcessMeta struct {
	OrigW    int
	OrigH    int
	ResizedW int
	ResizedH int
	PadX     int
	PadY     int
}

func resizeBilinear(src image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	srcBounds := src.Bounds()
	srcW, srcH := srcBounds.Dx(), srcBounds.Dy()

	xRatio := float64(srcW-1) / float64(w)
	if w <= 1 {
		xRatio = 0
	}
	yRatio := float64(srcH-1) / float64(h)
	if h <= 1 {
		yRatio = 0
	}

	for y := 0; y < h; y++ {
		ySrc := yRatio * float64(y)
		yLow := int(ySrc)
		yHigh := yLow + 1
		if yHigh >= srcH {
			yHigh = yLow
		}
		yWeight := ySrc - float64(yLow)

		for x := 0; x < w; x++ {
			xSrc := xRatio * float64(x)
			xLow := int(xSrc)
			xHigh := xLow + 1
			if xHigh >= srcW {
				xHigh = xLow
			}
			xWeight := xSrc - float64(xLow)

			c00 := src.At(srcBounds.Min.X+xLow, srcBounds.Min.Y+yLow)
			c10 := src.At(srcBounds.Min.X+xHigh, srcBounds.Min.Y+yLow)
			c01 := src.At(srcBounds.Min.X+xLow, srcBounds.Min.Y+yHigh)
			c11 := src.At(srcBounds.Min.X+xHigh, srcBounds.Min.Y+yHigh)

			r00, g00, b00, a00 := c00.RGBA()
			r10, g10, b10, a10 := c10.RGBA()
			r01, g01, b01, a01 := c01.RGBA()
			r11, g11, b11, a11 := c11.RGBA()

			interp := func(v00, v10, v01, v11 uint32) uint8 {
				top := float64(v00)*(1-xWeight) + float64(v10)*xWeight
				bottom := float64(v01)*(1-xWeight) + float64(v11)*xWeight
				return uint8((top*(1-yWeight) + bottom*yWeight) / 257)
			}

			dst.SetRGBA(x, y, color.RGBA{
				R: interp(r00, r10, r01, r11),
				G: interp(g00, g10, g01, g11),
				B: interp(b00, b10, b01, b11),
				A: interp(a00, a10, a01, a11),
			})
		}
	}
	return dst
}

func padImage(src image.Image, padX, padY int, fill color.Color) *image.RGBA {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	dstW, dstH := w+padX*2, h+padY*2
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))

	r, g, b, a := fill.RGBA()
	c := color.RGBA{R: uint8(r / 257), G: uint8(g / 257), B: uint8(b / 257), A: uint8(a / 257)}
	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW; x++ {
			dst.SetRGBA(x, y, c)
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(x+padX, y+padY, src.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	return dst
}

func decodeImageRobust(imgBytes []byte) (image.Image, error) {
	src, _, err := image.Decode(bytes.NewReader(imgBytes))
	if err == nil {
		return src, nil
	}

	if g, gifErr := gif.DecodeAll(bytes.NewReader(imgBytes)); gifErr == nil && len(g.Image) > 0 {
		firstFrame := g.Image[0]
		b := firstFrame.Bounds()
		rgba := image.NewRGBA(b)
		draw.Draw(rgba, b, firstFrame, b.Min, draw.Src)
		return rgba, nil
	}

	if len(imgBytes) > 12 {
		magic := string(imgBytes[4:12])
		if strings.Contains(magic, "heic") || strings.Contains(magic, "heif") || strings.Contains(magic, "mif1") {
			return nil, fmt.Errorf("检测到 Apple HEIC 格式图片。请截图或转换为 JPEG/PNG 上传")
		}
	}

	return nil, fmt.Errorf("无法解码上传的图片格式，仅支持 JPEG/PNG/WebP/GIF: %w", err)
}

func preprocessImage(imgBytes []byte, mode string) ([]byte, ProcessMeta, error) {
	src, err := decodeImageRobust(imgBytes)
	if err != nil {
		return nil, ProcessMeta{}, err
	}

	bounds := src.Bounds()
	origW, origH := bounds.Dx(), bounds.Dy()
	w, h := origW, origH

	meta := ProcessMeta{OrigW: origW, OrigH: origH}
	var processed image.Image = src

	if mode == "face" {
		minDim := w
		if h < minDim {
			minDim = h
		}
		if minDim < 320 {
			scale := 320.0 / float64(minDim)
			processed = resizeBilinear(processed, int(float64(w)*scale), int(float64(h)*scale))
			w, h = processed.Bounds().Dx(), processed.Bounds().Dy()
		}

		maxDim := w
		if h > maxDim {
			maxDim = h
		}
		if maxDim > 640 {
			scale := 640.0 / float64(maxDim)
			processed = resizeBilinear(processed, int(float64(w)*scale), int(float64(h)*scale))
			w, h = processed.Bounds().Dx(), processed.Bounds().Dy()
		}

		alignW := int(math.Round(float64(w)/32.0)) * 32
		if alignW < 320 {
			alignW = 320
		}
		alignH := int(math.Round(float64(h)/32.0)) * 32
		if alignH < 320 {
			alignH = 320
		}
		if alignW != w || alignH != h {
			processed = resizeBilinear(processed, alignW, alignH)
			w, h = alignW, alignH
		}
		meta.ResizedW = w
		meta.ResizedH = h

		rawPadX := int(float64(w) * 0.15)
		if rawPadX < 16 {
			rawPadX = 16
		}
		padX := (((w + rawPadX*2 + 31) / 32) * 32 - w) / 2
		rawPadY := int(float64(h) * 0.15)
		if rawPadY < 16 {
			rawPadY = 16
		}
		padY := (((h + rawPadY*2 + 31) / 32) * 32 - h) / 2
		meta.PadX = padX
		meta.PadY = padY

		bgColor := processed.At(processed.Bounds().Min.X, processed.Bounds().Min.Y)
		processed = padImage(processed, padX, padY, bgColor)
	} else {
		maxDim := w
		if h > maxDim {
			maxDim = h
		}
		scale := 1.0
		if maxDim > 512 {
			scale = 512.0 / float64(maxDim)
		}
		clipW := int(math.Round(float64(w)*scale/32.0)) * 32
		clipH := int(math.Round(float64(h)*scale/32.0)) * 32
		if clipW < 32 {
			clipW = 32
		}
		if clipH < 32 {
			clipH = 32
		}
		if clipW != w || clipH != h {
			processed = resizeBilinear(processed, clipW, clipH)
		}
		meta.ResizedW = processed.Bounds().Dx()
		meta.ResizedH = processed.Bounds().Dy()
	}

	buf := new(bytes.Buffer)
	if err := jpeg.Encode(buf, processed, &jpeg.Options{Quality: 88}); err != nil {
		return nil, meta, fmt.Errorf("JPEG 编码失败: %w", err)
	}
	return buf.Bytes(), meta, nil
}

type FaceBoundingBox struct {
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
	X2 float64 `json:"x2"`
	Y2 float64 `json:"y2"`
}

type FaceItem struct {
	Index       int             `json:"index"`
	Score       float64         `json:"score"`
	BoundingBox FaceBoundingBox `json:"boundingBox"`
	embedding   []float32
}

func extractEmbedding(compressedBytes []byte, mode string, meta ProcessMeta, minScore float64, faceIndex int) ([]float32, []FaceItem, int, error) {
	var entriesJSON string
	if mode == "face" {
		payload := map[string]interface{}{
			"facial-recognition": map[string]interface{}{
				"detection": map[string]interface{}{
					"modelName": cfg.FaceModelName,
					"options": map[string]interface{}{
						"minScore": minScore,
					},
				},
				"recognition": map[string]interface{}{
					"modelName": cfg.FaceModelName,
				},
			},
		}
		b, _ := json.Marshal(payload)
		entriesJSON = string(b)
	} else {
		payload := map[string]interface{}{
			"clip": map[string]interface{}{
				"visual": map[string]interface{}{
					"modelName": cfg.CLIPModelName,
				},
			},
		}
		b, _ := json.Marshal(payload)
		entriesJSON = string(b)
	}

	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	_ = writer.WriteField("entries", entriesJSON)
	part, err := writer.CreateFormFile("image", "image.jpg")
	if err != nil {
		return nil, nil, 0, err
	}
	_, _ = part.Write(compressedBytes)
	_ = writer.Close()

	req, err := http.NewRequest("POST", cfg.MLPredictURL, body)
	if err != nil {
		return nil, nil, 0, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	log.Printf("[ML-QUEUE] 正在排队等待获取 ML 算力令牌 (mode=%s)...", mode)
	select {
	case mlSemaphore <- struct{}{}:
		log.Printf("[ML-QUEUE] 成功获取算力令牌，开始执行推理 (mode=%s)", mode)
		defer func() {
			time.Sleep(150 * time.Millisecond)
			<-mlSemaphore
			log.Printf("[ML-QUEUE] 推理完成且冷却 150ms 完毕，已释放算力令牌 (mode=%s)", mode)
		}()
	case <-time.After(25 * time.Second):
		log.Printf("[ML-QUEUE] 排队等待超时 (25s)，放弃本次推理 (mode=%s)", mode)
		return nil, nil, 0, fmt.Errorf("AI 推理引擎队列繁忙，请稍后重试")
	}

	// 放宽超时到 180 秒，容纳大模型首次加载与冷启动开销
	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "Client.Timeout") || strings.Contains(err.Error(), "deadline exceeded") {
			return nil, nil, 0, fmt.Errorf("机器学习模型冷启动加载超时 (已等待 180s)，模型可能正在载入内存，请稍后点击重试")
		}
		return nil, nil, 0, fmt.Errorf("无法连接 ML 预测服务 (%s): %w", cfg.MLPredictURL, err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		rawResp := strings.TrimSpace(string(respBytes))
		log.Printf("[ML-ERROR-DEBUG] 预测服务异常 HTTP %d\n"+
			"  ├─ 请求端点: %s\n"+
			"  ├─ 模式参数: mode=%s, minScore=%.2f, faceIndex=%d\n"+
			"  ├─ 图片体积: %d 字节 (原图: %dx%d, 缩放: %dx%d)\n"+
			"  ├─ entries载荷: %s\n"+
			"  ├─ 上游原始响应: %s\n"+
			"  └─ Go调用栈:\n%s",
			resp.StatusCode, cfg.MLPredictURL, mode, minScore, faceIndex,
			len(compressedBytes), meta.OrigW, meta.OrigH, meta.ResizedW, meta.ResizedH,
			entriesJSON, rawResp, string(debug.Stack()))
		return nil, nil, 0, fmt.Errorf("ML 推理失败 [%d]: %s", resp.StatusCode, rawResp)
	}

	var rawMap map[string]interface{}
	if err := json.Unmarshal(respBytes, &rawMap); err != nil {
		return nil, nil, 0, fmt.Errorf("ML 响应反序列化失败: %w", err)
	}

	if mode == "face" {
		faceField, ok := rawMap["facial-recognition"]
		if !ok {
			return nil, nil, 0, fmt.Errorf("ML 响应缺少 facial-recognition 结果")
		}

		var rawFaces []map[string]interface{}
		switch val := faceField.(type) {
		case string:
			_ = json.Unmarshal([]byte(val), &rawFaces)
		case []interface{}:
			for _, item := range val {
				if m, ok := item.(map[string]interface{}); ok {
					rawFaces = append(rawFaces, m)
				}
			}
		}

		if len(rawFaces) == 0 {
			return nil, nil, 0, fmt.Errorf("未识别到人脸 (置信度 >= %.2f)", minScore)
		}

		var detectedFaces []FaceItem
		bestIndex := 0
		maxScore := -1.0

		for idx, rf := range rawFaces {
			score, _ := rf["score"].(float64)
			emb, err := parseEmbeddingArray(rf["embedding"])
			if err != nil {
				continue
			}

			var normBox FaceBoundingBox
			if bboxMap, ok := rf["boundingBox"].(map[string]interface{}); ok {
				x1, _ := bboxMap["x1"].(float64)
				y1, _ := bboxMap["y1"].(float64)
				x2, _ := bboxMap["x2"].(float64)
				y2, _ := bboxMap["y2"].(float64)

				normBox.X1 = math.Max(0, math.Min(1, (x1-float64(meta.PadX))/float64(meta.ResizedW)))
				normBox.Y1 = math.Max(0, math.Min(1, (y1-float64(meta.PadY))/float64(meta.ResizedH)))
				normBox.X2 = math.Max(0, math.Min(1, (x2-float64(meta.PadX))/float64(meta.ResizedW)))
				normBox.Y2 = math.Max(0, math.Min(1, (y2-float64(meta.PadY))/float64(meta.ResizedH)))
			}

			detectedFaces = append(detectedFaces, FaceItem{
				Index:       idx,
				Score:       math.Round(score*1000) / 1000,
				BoundingBox: normBox,
				embedding:   emb,
			})

			if score > maxScore {
				maxScore = score
				bestIndex = idx
			}
		}

		selectedIndex := bestIndex
		if faceIndex >= 0 && faceIndex < len(detectedFaces) {
			selectedIndex = faceIndex
		}

		return detectedFaces[selectedIndex].embedding, detectedFaces, selectedIndex, nil
	}

	clipRaw, ok := rawMap["clip"]
	if !ok {
		clipRaw = rawMap["visual"]
	}
	emb, err := parseEmbeddingArray(clipRaw)
	return emb, nil, 0, err
}

func parseEmbeddingArray(val interface{}) ([]float32, error) {
	var rawList []interface{}
	switch v := val.(type) {
	case string:
		if err := json.Unmarshal([]byte(v), &rawList); err != nil {
			return nil, fmt.Errorf("向量解析失败: %w", err)
		}
	case []interface{}:
		rawList = v
	default:
		return nil, fmt.Errorf("未知向量格式: %T", val)
	}

	res := make([]float32, len(rawList))
	for i, item := range rawList {
		if f, ok := item.(float64); ok {
			res[i] = float32(f)
		}
	}
	return res, nil
}

type SearchResult struct {
	AssetID          string  `json:"assetId"`
	Similarity       float64 `json:"similarity"`
	SimilarityPct    float64 `json:"similarity_pct"`
	OriginalFileName string  `json:"originalFileName"`
	DateStr          string  `json:"date_str"`
	ImmichURL        string  `json:"immich_url"`
}

func searchSimilarStrict(ctx context.Context, embedding []float32, mode string, topK int, threshold float64, allowedOwnerIDs []string) ([]SearchResult, error) {
	results := make([]SearchResult, 0)
	if len(allowedOwnerIDs) == 0 {
		return results, fmt.Errorf("未检测到有效相册访问权限或鉴权未就绪")
	}

	var sb strings.Builder
	sb.WriteString("[")
	for i, v := range embedding {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(strconv.FormatFloat(float64(v), 'f', -1, 32))
	}
	sb.WriteString("]")
	vecStr := sb.String()

	queryCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var querySQL string
	var args []interface{}
	thresholdVal := threshold / 100.0

	args = append(args, vecStr, allowedOwnerIDs)

	if mode == "face" {
		havingClause := ""
		argIdx := 3
		if thresholdVal > 0 {
			havingClause = fmt.Sprintf(`HAVING 1 - (MIN(fs.embedding <=> $1::vector)) >= $%d`, argIdx)
			args = append(args, thresholdVal)
			argIdx++
		}
		limitClause := fmt.Sprintf(`LIMIT $%d`, argIdx)
		args = append(args, topK)

		querySQL = fmt.Sprintf(`
		SELECT 
			af."assetId",
			1 - (MIN(fs.embedding <=> $1::vector)) AS similarity,
			a."originalFileName",
			COALESCE(a."localDateTime", a."fileCreatedAt") AS photo_time
		FROM public."face_search" fs
		JOIN public."asset_face" af ON fs."faceId" = af."id"
		JOIN public."asset" a ON af."assetId" = a."id"
		WHERE a."deletedAt" IS NULL
		  AND a."isOffline" = false
		  AND (a."ownerId"::text = ANY($2) OR a."id" IN (SELECT aa."assetId" FROM public."album_asset" aa JOIN public."album_user" au ON aa."albumId" = au."albumId" WHERE au."userId"::text = ANY($2)))
		GROUP BY af."assetId", a."originalFileName", a."localDateTime", a."fileCreatedAt"
		%s
		ORDER BY MIN(fs.embedding <=> $1::vector) ASC
		%s;`, havingClause, limitClause)
	} else {
		thresholdClause := ""
		argIdx := 3
		if thresholdVal > 0 {
			thresholdClause = fmt.Sprintf(`AND 1 - (s.embedding <=> $1::vector) >= $%d`, argIdx)
			args = append(args, thresholdVal)
			argIdx++
		}
		limitClause := fmt.Sprintf(`LIMIT $%d`, argIdx)
		args = append(args, topK)

		querySQL = fmt.Sprintf(`
		SELECT 
			s."assetId",
			1 - (s.embedding <=> $1::vector) AS similarity,
			a."originalFileName",
			COALESCE(a."localDateTime", a."fileCreatedAt") AS photo_time
		FROM public."smart_search" s
		JOIN public."asset" a ON s."assetId" = a."id"
		WHERE a."deletedAt" IS NULL
		  AND a."isOffline" = false
		  AND (a."ownerId"::text = ANY($2) OR a."id" IN (SELECT aa."assetId" FROM public."album_asset" aa JOIN public."album_user" au ON aa."albumId" = au."albumId" WHERE au."userId"::text = ANY($2)))
		  %s
		ORDER BY s.embedding <=> $1::vector ASC
		%s;`, thresholdClause, limitClause)
	}

	rows, err := dbPool.Query(queryCtx, querySQL, args...)
	if err != nil {
		return results, err
	}
	defer rows.Close()

	for rows.Next() {
		var r SearchResult
		var photoTime *time.Time
		if err := rows.Scan(&r.AssetID, &r.Similarity, &r.OriginalFileName, &photoTime); err != nil {
			return results, err
		}
		r.SimilarityPct = math.Round(r.Similarity*10000) / 100
		if photoTime != nil {
			r.DateStr = photoTime.Format("2006-01-02 15:04:05")
		} else {
			r.DateStr = "未知"
		}
		r.ImmichURL = fmt.Sprintf("/photos/%s", r.AssetID)
		results = append(results, r)
	}

	if err := rows.Err(); err != nil {
		return results, fmt.Errorf("数据库向量检索读取中断: %w", err)
	}

	return results, nil
}

func parseKeyList(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	})
	var cleaned []string
	seen := make(map[string]bool)
	for _, p := range parts {
		k := strings.TrimSpace(p)
		if k != "" && !seen[k] {
			seen[k] = true
			cleaned = append(cleaned, k)
		}
	}
	return cleaned
}

func main() {
	initConfig()
	initDB()

	// 从嵌入的 FS 中提取 dist 目录作为静态根
	distFS, err := fs.Sub(distEmbedFS, "dist")
	if err != nil {
		log.Fatalf("无法加载嵌入的前端资源: %v", err)
	}

	// 提前在内存中读取 index.html 内容，避免经过 http.FileServer 产生 301 递归死循环
	indexHTML, err := fs.ReadFile(distFS, "index.html")
	if err != nil {
		log.Fatalf("无法读取嵌入的 index.html: %v", err)
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	r.MaxMultipartMemory = 12 << 20

	// 智能 CORS：支持携带凭据
	r.Use(func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		reqHost := c.Request.Host
		isAllowed := origin == ""
		if u, err := url.Parse(origin); err == nil && origin != "" {
			reqHostname := reqHost
			if h, _, err := net.SplitHostPort(reqHost); err == nil {
				reqHostname = h
			}
			if strings.EqualFold(u.Host, reqHost) || strings.EqualFold(u.Hostname(), reqHostname) {
				isAllowed = true
			}
		}
		if isAllowed && origin != "" {
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		} else if isAllowed {
			c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		}
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Authorization, X-Immich-Api-Key, Cookie")
		if c.Request.Method == "OPTIONS" {
			if !isAllowed && origin != "" {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.AbortWithStatus(204)
			return
		}
		if c.Request.Method == "POST" {
			if c.Request.ContentLength > 12<<20 {
				c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"detail": "上传图片体积超出限制 (最大 12MB)"})
				return
			}
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 12<<20)
		}
		c.Next()
	})

	// 核心业务处理函数
	handleAuthMe := func(c *gin.Context) {
		user, err := resolveUserFromCookie(c.Request)
		if err != nil {
			c.JSON(200, gin.H{"authenticated": false, "detail": err.Error()})
			return
		}
		c.JSON(200, gin.H{"authenticated": true, "user": user})
	}

	handleAuthValidate := func(c *gin.Context) {
		var req struct {
			APIKey string `json:"api_key"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.APIKey) == "" {
			c.JSON(400, gin.H{"detail": "缺少待验证的 api_key"})
			return
		}
		trimmedKey := strings.TrimSpace(req.APIKey)
		compareSecret := os.Getenv("IMMICH_COMPARE_API_KEY")
		if compareSecret != "" && trimmedKey == compareSecret {
			c.JSON(200, gin.H{"valid": true, "user": gin.H{"id": "compare-guest", "name": "人脸比对专属授权", "email": "compare@guest"}})
			return
		}
		user, err := resolveUser(trimmedKey)
		if err != nil {
			c.JSON(401, gin.H{"valid": false, "detail": err.Error()})
			return
		}
		c.JSON(200, gin.H{"valid": true, "user": user})
	}

	handleSearch := func(c *gin.Context) {
		t0 := time.Now()

		var validUsers []UserIdentity
		var userIDs []string

		// 1. 优先尝试从同域 Cookie 中获取登录身份
		if cookieUser, err := resolveUserFromCookie(c.Request); err == nil {
			validUsers = append(validUsers, cookieUser)
			userIDs = append(userIDs, cookieUser.UserID)
		}

		// 2. 检查是否有附带的 API Key(s)（支持作为补充或降级兜底）
		rawKeysStr := c.GetHeader("X-Immich-Api-Key")
		if rawKeysStr == "" {
			rawKeysStr = c.DefaultPostForm("api_keys", c.DefaultPostForm("api_key", c.Query("api_key")))
		}
		apiKeys := parseKeyList(rawKeysStr)
		if len(apiKeys) > 0 {
			if keyUsers, keyIDs, err := validateMultipleKeys(apiKeys); err == nil {
				seen := make(map[string]bool)
				for _, id := range userIDs {
					seen[id] = true
				}
				for i, id := range keyIDs {
					if !seen[id] {
						seen[id] = true
						userIDs = append(userIDs, id)
						validUsers = append(validUsers, keyUsers[i])
					}
				}
			}
		}

		// 3. 严格安全门禁：两项皆空时拦截
		if len(validUsers) == 0 {
			c.JSON(401, gin.H{"detail": "【安全拦截】未检测到 Immich 会话登录态，且未配置有效的 API Key"})
			return
		}

		allowedOwnerMap := make(map[string]bool)
		for _, id := range userIDs {
			allowedOwnerMap[id] = true
		}
		allowedOwnerIDs := make([]string, 0, len(allowedOwnerMap))
		for id := range allowedOwnerMap {
			allowedOwnerIDs = append(allowedOwnerIDs, id)
		}

		mode := c.DefaultPostForm("mode", c.DefaultQuery("mode", "clip"))
		fileHeader, err := c.FormFile("file")
		if err != nil {
			if strings.Contains(err.Error(), "too large") {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"detail": "上传图片体积超出限制 (最大 12MB)"})
				return
			}
			c.JSON(400, gin.H{"detail": "缺少上传的图片文件 (file)"})
			return
		}

		topK, _ := strconv.Atoi(c.DefaultPostForm("top_k", c.DefaultQuery("top_k", "12")))
		if topK <= 0 {
			topK = 12
		}
		threshold, _ := strconv.ParseFloat(c.DefaultPostForm("threshold", c.DefaultQuery("threshold", "0")), 64)
		minScore, _ := strconv.ParseFloat(c.DefaultPostForm("min_score", c.DefaultQuery("min_score", "0.2")), 64)
		if minScore <= 0 {
			minScore = 0.2
		}
		faceIndex, _ := strconv.Atoi(c.DefaultPostForm("face_index", c.DefaultQuery("face_index", "-1")))

		if len(allowedOwnerIDs) == 0 {
			c.JSON(401, gin.H{"detail": "未检测到有效相册访问权限或鉴权未就绪"})
			return
		}

		f, err := fileHeader.Open()
		if err != nil {
			c.JSON(500, gin.H{"detail": "读取图片流异常"})
			return
		}
		defer f.Close()

		rawBytes, _ := io.ReadAll(f)

		imgHash := c.GetHeader("X-Image-Hash")
		if len(imgHash) != 64 {
			imgHash = c.PostForm("image_hash")
		}
		if len(imgHash) != 64 {
			hasher := sha256.New()
			hasher.Write(rawBytes)
			imgHash = hex.EncodeToString(hasher.Sum(nil))
		}
		cacheKey := fmt.Sprintf("%s:%s:%.2f", imgHash, mode, minScore)

		var embedding []float32
		var detectedFaces []FaceItem
		var selectedFaceIdx int
		var cacheHit bool

		if cachedObj, exists := inferenceCache.Load(cacheKey); exists {
			cached := cachedObj.(CachedInference)
			detectedFaces = cached.DetectedFaces
			selectedFaceIdx = cached.SelectedFace
			embedding = cached.Embedding

			if mode == "face" && faceIndex >= 0 && faceIndex < len(detectedFaces) {
				selectedFaceIdx = faceIndex
				embedding = detectedFaces[faceIndex].embedding
			}
			cacheHit = true
		}

		if !cacheHit {
			compressed, meta, err := preprocessImage(rawBytes, mode)
			if err != nil {
				log.Printf("[SEARCH-ERROR] 图片预处理异常 (mode=%s, bytes=%d): %v", mode, len(rawBytes), err)
				c.JSON(400, gin.H{"detail": err.Error()})
				return
			}

			embedding, detectedFaces, selectedFaceIdx, err = extractEmbedding(compressed, mode, meta, minScore, faceIndex)
			if err != nil {
				status := 400
				if strings.Contains(err.Error(), "ML 推理失败") || strings.Contains(err.Error(), "无法连接") {
					status = 502
				}
				log.Printf("[SEARCH-WARN] 搜图业务中断 (HTTP %d): %v\n"+
					"  ├─ 检索模式: mode=%s, minScore=%.2f, faceIndex=%d\n"+
					"  ├─ 图像尺寸: %dx%d -> %dx%d (pad: %d,%d)\n"+
					"  └─ 授权用户: %v",
					status, err, mode, minScore, faceIndex,
					meta.OrigW, meta.OrigH, meta.ResizedW, meta.ResizedH, meta.PadX, meta.PadY, allowedOwnerIDs)
				c.JSON(status, gin.H{"detail": err.Error()})
				return
			}

			inferenceCache.Store(cacheKey, CachedInference{
				Embedding:     embedding,
				DetectedFaces: detectedFaces,
				SelectedFace:  selectedFaceIdx,
				CreatedAt:     time.Now(),
			})
		}

		results, err := searchSimilarStrict(c.Request.Context(), embedding, mode, topK, threshold, allowedOwnerIDs)
		if err != nil {
			log.Printf("[SEARCH-ERROR] 向量数据库检索异常 (mode=%s, topK=%d, threshold=%.2f): %v",
				mode, topK, threshold, err)
			c.JSON(500, gin.H{"detail": fmt.Sprintf("检索失败: %v", err)})
			return
		}

		c.JSON(200, gin.H{
			"mode":                mode,
			"cost_ms":             math.Round(float64(time.Since(t0).Microseconds())/100.0) / 10.0,
			"cache_hit":           cacheHit,
			"detected_faces":      detectedFaces,
			"selected_face_index": selectedFaceIdx,
			"results":             results,
			"authenticated_users": validUsers,
		})
		log.Printf("[SEARCH-TRACE] mode=%s cost=%v results=%d owners=%v",
			mode, time.Since(t0), len(results), allowedOwnerIDs)
	}

	handleThumbnail := func(c *gin.Context) {
		assetID := c.Param("asset_id")
		client := &http.Client{Timeout: 5 * time.Second}

		candidateURLs := []string{
			fmt.Sprintf("%s/api/assets/%s/thumbnail?size=thumbnail", cfg.ImmichAPIURL, assetID),
			fmt.Sprintf("%s/api/assets/%s/thumbnail?size=preview", cfg.ImmichAPIURL, assetID),
			fmt.Sprintf("%s/api/assets/%s/thumbnail", cfg.ImmichAPIURL, assetID),
		}

		// 1. 优先使用 Cookie 代理获取缩略图
		cookieHeader := c.Request.Header.Get("Cookie")
		authHeader := c.Request.Header.Get("Authorization")
		if cookieHeader != "" || authHeader != "" {
			for _, targetURL := range candidateURLs {
				req, err := http.NewRequest("GET", targetURL, nil)
				if err != nil {
					continue
				}
				if cookieHeader != "" {
					req.Header.Set("Cookie", cookieHeader)
				}
				if authHeader != "" {
					req.Header.Set("Authorization", authHeader)
				}
				resp, err := client.Do(req)
				if err == nil && resp.StatusCode == 200 {
					defer resp.Body.Close()
					c.Header("Cache-Control", "public, max-age=604800, immutable")
					c.DataFromReader(200, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
					return
				}
				if resp != nil {
					resp.Body.Close()
				}
			}
		}

		// 2. 回退到 API Key 轮询
		rawKeysStr := c.GetHeader("X-Immich-Api-Key")
		if rawKeysStr == "" {
			rawKeysStr = c.Query("api_key")
		}
		apiKeys := parseKeyList(rawKeysStr)

		for _, key := range apiKeys {
			for _, targetURL := range candidateURLs {
				req, err := http.NewRequest("GET", targetURL, nil)
				if err != nil {
					continue
				}
				req.Header.Set("x-api-key", key)
				resp, err := client.Do(req)
				if err == nil && resp.StatusCode == 200 {
					defer resp.Body.Close()
					c.Header("Cache-Control", "public, max-age=604800, immutable")
					c.DataFromReader(200, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
					return
				}
				if resp != nil {
					resp.Body.Close()
				}
			}
		}

		c.Status(http.StatusNotFound)
	}

	handleWarmup := func(c *gin.Context) {
		isAuthed := false
		if _, err := resolveUserFromCookie(c.Request); err == nil {
			isAuthed = true
		} else {
			rawKeysStr := c.GetHeader("X-Immich-Api-Key")
			if rawKeysStr == "" {
				rawKeysStr = c.DefaultPostForm("api_keys", c.DefaultPostForm("api_key", c.Query("api_key")))
			}
			keyList := parseKeyList(rawKeysStr)
			compareSecret := os.Getenv("IMMICH_COMPARE_API_KEY")
			for _, k := range keyList {
				if compareSecret != "" && k == compareSecret {
					isAuthed = true
					break
				}
			}
			if !isAuthed && len(keyList) > 0 {
				if _, _, err := validateMultipleKeys(keyList); err == nil {
					isAuthed = true
				}
			}
		}
		if !isAuthed {
			c.JSON(http.StatusUnauthorized, gin.H{"detail": "【安全拦截】预热需要验证 Immich 会话或 API Key"})
			return
		}

		mode := c.DefaultQuery("mode", "clip")
		warmupLock.Lock()
		defer warmupLock.Unlock()

		if mode == "face" {
			if time.Since(lastFaceWarmup) < 3*time.Minute {
				c.JSON(200, gin.H{"status": "warm", "mode": "face"})
				return
			}
			lastFaceWarmup = time.Now()
		} else {
			if time.Since(lastClipWarmup) < 3*time.Minute {
				c.JSON(200, gin.H{"status": "warm", "mode": "clip"})
				return
			}
			lastClipWarmup = time.Now()
		}

		go func(targetMode string) {
			dummyJpg := []byte{
				0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01,
				0x01, 0x01, 0x00, 0x48, 0x00, 0x48, 0x00, 0x00, 0xff, 0xdb, 0x00, 0x43,
				0x00, 0x08, 0x06, 0x06, 0x07, 0x06, 0x05, 0x08, 0x07, 0x07, 0x07, 0x09,
				0x09, 0x08, 0x0a, 0x0c, 0x14, 0x0d, 0x0c, 0x0b, 0x0b, 0x0c, 0x19, 0x12,
				0x13, 0x0f, 0x14, 0x1d, 0x1a, 0x1f, 0x1e, 0x1d, 0x1a, 0x1c, 0x1c, 0x20,
				0x24, 0x2e, 0x27, 0x20, 0x22, 0x2c, 0x23, 0x1c, 0x1c, 0x28, 0x37, 0x29,
				0x2c, 0x30, 0x31, 0x34, 0x34, 0x34, 0x1f, 0x27, 0x39, 0x3d, 0x38, 0x32,
				0x3c, 0x2e, 0x33, 0x34, 0x32, 0xff, 0xc0, 0x00, 0x0b, 0x08, 0x00, 0x01,
				0x00, 0x01, 0x01, 0x01, 0x11, 0x00, 0xff, 0xc4, 0x00, 0x1f, 0x00, 0x00,
				0x01, 0x05, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
				0x09, 0x0a, 0x0b, 0xff, 0xda, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3f,
				0x00, 0xbf, 0x80, 0xff, 0xd9,
			}
			_ = dummyJpg
			dummyImg := image.NewRGBA(image.Rect(0, 0, 128, 128))
			buf := new(bytes.Buffer)
			_ = jpeg.Encode(buf, dummyImg, &jpeg.Options{Quality: 50})
			meta := ProcessMeta{OrigW: 128, OrigH: 128, ResizedW: 128, ResizedH: 128}
			_, _, _, warmupErr := extractEmbedding(buf.Bytes(), targetMode, meta, 0.2, -1)
			if warmupErr != nil {
				log.Printf("[WARMUP] 机器学习服务预热完成 (mode=%s, info: %v)", targetMode, warmupErr)
			} else {
				log.Printf("[WARMUP] 机器学习服务预热成功 (mode=%s)", targetMode)
			}
		}(mode)

		c.JSON(200, gin.H{"status": "warming", "mode": mode})
	}


	// 注册 API 路由：在 registerRoutes 闭包内新增 face-compare 端点
	registerRoutes := func(rg *gin.RouterGroup) {
		rg.GET("/api/auth/me", handleAuthMe)
		rg.POST("/api/auth/validate", handleAuthValidate)
		rg.POST("/api/search", handleSearch)
		rg.GET("/api/thumbnail/:asset_id", handleThumbnail)
		rg.GET("/api/warmup", handleWarmup)
		rg.POST("/api/face-compare", handleFaceCompare) // 新增：双人脸 1:1 比对接口
	}

	handleInjectJS := func(c *gin.Context) {
		candidates := []string{
			"inject.js",
			"../inject.js",
			"mine-search/inject.js",
			"SearchPlus2026/inject.js",
			"searchplus2026/inject.js",
		}
		var content []byte
		var err error
		for _, p := range candidates {
			content, err = os.ReadFile(p)
			if err == nil {
				break
			}
		}
		if err != nil {
			content, err = fs.ReadFile(distFS, "inject.js")
		}
		if err != nil {
			c.Header("Content-Type", "application/javascript; charset=utf-8")
			c.String(http.StatusNotFound, "// inject.js not found: %v", err)
			return
		}
		c.Header("Content-Type", "application/javascript; charset=utf-8")
		c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
		c.Data(http.StatusOK, "application/javascript; charset=utf-8", content)
	}

	r.GET("/inject.js", handleInjectJS)
	r.GET("/searchplus2026/inject.js", handleInjectJS)
	r.GET("/SearchPlus2026/inject.js", handleInjectJS)

	registerRoutes(&r.RouterGroup)
	registerRoutes(r.Group("/searchplus2026"))
	registerRoutes(r.Group("/SearchPlus2026"))

	// 静态文件与 SPA 路由兜底分发
	r.NoRoute(func(c *gin.Context) {
		reqPath := c.Request.URL.Path

		// 1. 访问 /mine-search 时补齐末尾斜杠，确保前端相对路径 (./assets/...) 正常解析
		if reqPath == "/searchplus2026" || reqPath == "/SearchPlus2026" {
			c.Redirect(http.StatusMovedPermanently, reqPath+"/")
			return
		}

		// 2. 剥除子路径前缀
		relPath := strings.TrimPrefix(reqPath, "/searchplus2026")
		relPath = strings.TrimPrefix(relPath, "/SearchPlus2026")
		relPath = strings.TrimPrefix(relPath, "/")

		// 3. API 路由未命中直接 404
		if strings.HasPrefix(relPath, "api/") {
			c.JSON(http.StatusNotFound, gin.H{"detail": "API 端点不存在"})
			return
		}

		// 4. 提供静态资源文件 (js、css、图片等)，明确排除 index.html 避免触发底层 301
		if relPath != "" && relPath != "index.html" {
			if f, err := distFS.Open(relPath); err == nil {
				_ = f.Close()
				if strings.HasPrefix(relPath, "assets/") {
					c.Header("Cache-Control", "public, max-age=31536000, immutable")
				}
				c.FileFromFS(relPath, http.FS(distFS))
				return
			}
		}

		// 5. SPA 单页兜底：直接输出 index.html 的内容并返回 HTTP 200，杜绝任何 301 循环
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
	})

	port := getEnv("PORT", "1880")
	log.Printf("[✓] Immich SearchPlus2026 服务已就绪，正在监听 :%s (适配子路径 /searchplus2026)...", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("启动失败: %v", err)
	}
}