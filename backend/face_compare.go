package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// 向量余弦相似度计算
type compareRateLimiter struct {
	sync.Mutex
	records map[string][]time.Time
}

var faceCompareLimiter = &compareRateLimiter{records: make(map[string][]time.Time)}

func (l *compareRateLimiter) allow(key string, limit int, window time.Duration) (bool, time.Duration) {
	l.Lock()
	defer l.Unlock()
	now := time.Now()
	cutoff := now.Add(-window)
	timestamps := l.records[key]
	var valid []time.Time
	for _, t := range timestamps {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	if len(valid) >= limit {
		oldest := valid[0]
		retryAfter := window - now.Sub(oldest)
		l.records[key] = valid
		return false, retryAfter
	}
	l.records[key] = append(valid, now)
	return true, 0
}

func computeCosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dotProduct, normA, normB float64
	for i := range a {
		va := float64(a[i])
		vb := float64(b[i])
		dotProduct += va * vb
		normA += va * va
		normB += vb * vb
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	sim := dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
	return math.Max(-1.0, math.Min(1.0, sim))
}

// 判定 AntelopeV2 / InsightFace 阈值置信等级
func evaluateFaceMatch(simPct float64) (string, string) {
	switch {
	case simPct >= 65.0:
		return "极高确信度为同一人", "strong"
	case simPct >= 50.0:
		return "大概率为同一人 (受角度/年龄/光线影响)", "likely"
	case simPct >= 35.0:
		return "特征部分相近，难以断定", "uncertain"
	default:
		return "极大概率不是同一人", "unlikely"
	}
}

// 抽取单张图片的人脸特征向量（含内存缓存）
func extractFaceFromFormFile(c *gin.Context, fileKey string, faceIdx int, minScore float64) ([]float32, []FaceItem, int, error) {
	fileHeader, err := c.FormFile(fileKey)
	if err != nil {
		if strings.Contains(err.Error(), "too large") {
			return nil, nil, 0, fmt.Errorf("上传图片体积超出限制 (最大 12MB)")
		}
		return nil, nil, 0, fmt.Errorf("缺少图片文件 (%s)", fileKey)
	}

	f, err := fileHeader.Open()
	if err != nil {
		return nil, nil, 0, fmt.Errorf("打开图片 (%s) 失败: %w", fileKey, err)
	}
	defer f.Close()

	rawBytes, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("读取图片 (%s) 失败: %w", fileKey, err)
	}

	// 计算 SHA-256 缓存键
	hasher := sha256.New()
	hasher.Write(rawBytes)
	imgHash := hex.EncodeToString(hasher.Sum(nil))
	cacheKey := fmt.Sprintf("%s:face:%.2f", imgHash, minScore)

	var embedding []float32
	var detectedFaces []FaceItem
	var selectedIdx int

	if cachedObj, exists := inferenceCache.Load(cacheKey); exists {
		cached := cachedObj.(CachedInference)
		detectedFaces = cached.DetectedFaces
		selectedIdx = cached.SelectedFace
		embedding = cached.Embedding

		if faceIdx >= 0 && faceIdx < len(detectedFaces) {
			selectedIdx = faceIdx
			embedding = detectedFaces[faceIdx].embedding
		}
		return embedding, detectedFaces, selectedIdx, nil
	}

	// 预处理并调用 ML 预测人脸
	compressed, meta, err := preprocessImage(rawBytes, "face")
	if err != nil {
		return nil, nil, 0, fmt.Errorf("[%s] 预处理失败: %w", fileKey, err)
	}

	embedding, detectedFaces, selectedIdx, err = extractEmbedding(compressed, "face", meta, minScore, faceIdx)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("[%s] %w", fileKey, err)
	}

	inferenceCache.Store(cacheKey, CachedInference{
		Embedding:     embedding,
		DetectedFaces: detectedFaces,
		SelectedFace:  selectedIdx,
		CreatedAt:     time.Now(),
	})

	return embedding, detectedFaces, selectedIdx, nil
}

// 核心比对控制器：handleFaceCompare
func handleFaceCompare(c *gin.Context) {
	t0 := time.Now()

	// 鉴权拦截：需具备有效会话或 API Key
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
		c.JSON(http.StatusUnauthorized, gin.H{"detail": "【安全拦截】执行人脸比对需要验证 Immich 凭据"})
		return
	}

	clientKey := c.ClientIP()
	if allowed, waitDur := faceCompareLimiter.allow(clientKey, 15, time.Minute); !allowed {
		waitSec := int(math.Ceil(waitDur.Seconds()))
		c.Header("Retry-After", strconv.Itoa(waitSec))
		c.JSON(http.StatusTooManyRequests, gin.H{"detail": fmt.Sprintf("人脸比对请求过于频繁，请在 %d 秒后重试", waitSec)})
		return
	}

	minScore, _ := strconv.ParseFloat(c.DefaultPostForm("min_score", "0.2"), 64)
	if minScore <= 0 {
		minScore = 0.2
	}
	faceIdxA, _ := strconv.Atoi(c.DefaultPostForm("face_index_a", "-1"))
	faceIdxB, _ := strconv.Atoi(c.DefaultPostForm("face_index_b", "-1"))

	// 提取图片 A 人脸
	embA, facesA, selA, errA := extractFaceFromFormFile(c, "file_a", faceIdxA, minScore)
	if errA != nil {
		status := http.StatusBadRequest
		if strings.Contains(errA.Error(), "最大 12MB") {
			status = http.StatusRequestEntityTooLarge
		}
		c.JSON(status, gin.H{"detail": fmt.Sprintf("图片 A 分析失败: %v", errA)})
		return
	}

	// 提取图片 B 人脸
	embB, facesB, selB, errB := extractFaceFromFormFile(c, "file_b", faceIdxB, minScore)
	if errB != nil {
		status := http.StatusBadRequest
		if strings.Contains(errB.Error(), "最大 12MB") {
			status = http.StatusRequestEntityTooLarge
		}
		c.JSON(status, gin.H{"detail": fmt.Sprintf("图片 B 分析失败: %v", errB)})
		return
	}

	// 计算点积余弦距离
	sim := computeCosineSimilarity(embA, embB)
	simPct := math.Round(math.Max(0, sim)*10000) / 100
	verdict, level := evaluateFaceMatch(simPct)

	c.JSON(http.StatusOK, gin.H{
		"similarity":       math.Round(sim*10000) / 10000,
		"similarity_pct":   simPct,
		"verdict":          verdict,
		"verdict_level":    level,
		"detected_faces_a": facesA,
		"selected_face_a":  selA,
		"detected_faces_b": facesB,
		"selected_face_b":  selB,
		"cost_ms":          math.Round(float64(time.Since(t0).Microseconds())/100.0) / 10.0,
	})
}