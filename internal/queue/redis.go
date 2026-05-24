package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/yesol-jam/vm-provisioning/internal/config"
	"github.com/yesol-jam/vm-provisioning/pkg/models"
	"github.com/sirupsen/logrus"
)

// RedisQueue Redis 기반 작업 큐
type RedisQueue struct {
	client    *redis.Client
	queueName string
	logger    *logrus.Logger
}

// NewRedisQueue 새 Redis 큐 생성
func NewRedisQueue(cfg *config.Config, logger *logrus.Logger) (*RedisQueue, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%s", cfg.RedisHost, cfg.RedisPort),
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})

	// 연결 테스트
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("Redis 연결 실패: %w", err)
	}

	logger.Info("Redis 연결 성공")

	return &RedisQueue{
		client:    client,
		queueName: cfg.QueueName,
		logger:    logger,
	}, nil
}

// Pop 큐에서 작업을 가져옵니다 (블로킹)
func (q *RedisQueue) Pop(ctx context.Context) (*models.TerraformJobPayload, error) {
	// BLPOP으로 작업 대기 (최대 30초)
	result, err := q.client.BLPop(ctx, 30*time.Second, q.queueName).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil // 타임아웃
		}
		return nil, fmt.Errorf("큐 읽기 실패: %w", err)
	}

	if len(result) < 2 {
		return nil, fmt.Errorf("잘못된 큐 데이터")
	}

	var job models.TerraformJobPayload
	if err := json.Unmarshal([]byte(result[1]), &job); err != nil {
		return nil, fmt.Errorf("작업 파싱 실패: %w", err)
	}

	q.logger.Infof("작업 수신: projectID=%d, executionID=%d, type=%s",
		job.ProjectID, job.ExecutionID, job.Type)

	return &job, nil
}

// Push 큐에 작업을 추가합니다 (테스트용)
func (q *RedisQueue) Push(ctx context.Context, job *models.TerraformJobPayload) error {
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("작업 직렬화 실패: %w", err)
	}

	if err := q.client.RPush(ctx, q.queueName, data).Err(); err != nil {
		return fmt.Errorf("큐 추가 실패: %w", err)
	}

	return nil
}

// Close Redis 연결 종료
func (q *RedisQueue) Close() error {
	return q.client.Close()
}

// QueueLength 큐 길이 조회
func (q *RedisQueue) QueueLength(ctx context.Context) (int64, error) {
	return q.client.LLen(ctx, q.queueName).Result()
}

// GetClient Redis 클라이언트 반환 (ReliableQueue에서 사용)
func (q *RedisQueue) GetClient() *redis.Client {
	return q.client
}
