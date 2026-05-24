package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/itraining/provisioning-worker/pkg/models"
	"github.com/sirupsen/logrus"
)

const (
	DefaultJobQueue        = "terraform:jobs"
	DefaultProcessingQueue = "terraform:processing"
	DefaultTimestampHash   = "terraform:processing:timestamps"
	DefaultStalledTimeout  = 30 * time.Minute  // VM 생성이 오래 걸릴 수 있음
)

// JobWithRawJSON 원본 JSON을 보존하는 작업 래퍼
// AckJob/NackJob에서 원본 JSON을 사용해야 LREM이 정확히 매칭됨
type JobWithRawJSON struct {
	Job     *models.TerraformJobPayload
	RawJSON string // Redis에서 받은 원본 JSON (Java 직렬화 형식 그대로)
}

// ReliableRedisQueue Redis Reliable Queue 구현
// RPOPLPUSH 패턴을 사용하여 작업 유실 방지
type ReliableRedisQueue struct {
	client          *redis.Client
	jobQueue        string
	processingQueue string
	timestampHash   string
	stalledTimeout  time.Duration
	logger          *logrus.Logger
}

// NewReliableRedisQueue 새 Reliable Redis 큐 생성
func NewReliableRedisQueue(client *redis.Client, logger *logrus.Logger) *ReliableRedisQueue {
	return &ReliableRedisQueue{
		client:          client,
		jobQueue:        DefaultJobQueue,
		processingQueue: DefaultProcessingQueue,
		timestampHash:   DefaultTimestampHash,
		stalledTimeout:  DefaultStalledTimeout,
		logger:          logger,
	}
}

// PopReliable 큐에서 작업을 안전하게 가져옵니다 (BRPOPLPUSH)
// jobs 큐에서 꺼내서 processing 큐로 이동
// 원본 JSON을 보존하여 AckJob/NackJob에서 정확한 매칭이 가능하도록 함
func (q *ReliableRedisQueue) PopReliable(ctx context.Context) (*JobWithRawJSON, error) {
	// BRPOPLPUSH: jobs → processing
	result, err := q.client.BRPopLPush(
		ctx,
		q.jobQueue,
		q.processingQueue,
		30*time.Second,
	).Result()

	if err != nil {
		if err == redis.Nil {
			return nil, nil // 타임아웃
		}
		return nil, fmt.Errorf("큐 읽기 실패: %w", err)
	}

	var job models.TerraformJobPayload
	if err := json.Unmarshal([]byte(result), &job); err != nil {
		// 파싱 실패한 작업은 processing 큐에서 제거
		q.client.LRem(ctx, q.processingQueue, 1, result)
		return nil, fmt.Errorf("작업 파싱 실패: %w", err)
	}

	// 작업 시작 시간 기록 (Stalled Job 복구를 위한 타임스탬프)
	timestamp := time.Now().Unix()
	q.client.HSet(ctx, q.timestampHash, fmt.Sprintf("%d", job.ExecutionID), timestamp)

	q.logger.Infof("작업 수신 (Reliable): executionID=%d, type=%s",
		job.ExecutionID, job.Type)

	// 원본 JSON을 함께 반환하여 AckJob에서 정확한 매칭 가능
	return &JobWithRawJSON{
		Job:     &job,
		RawJSON: result,
	}, nil
}

// AckJob 작업 완료 확인 (processing 큐에서 제거)
// 원본 JSON을 우선 시도하고, 실패 시 executionID로 검색하여 제거
func (q *ReliableRedisQueue) AckJob(ctx context.Context, jobWrapper *JobWithRawJSON) error {
	executionID := jobWrapper.Job.ExecutionID

	// 1차 시도: 원본 JSON으로 제거 (가장 빠름)
	removed, err := q.client.LRem(ctx, q.processingQueue, 1, jobWrapper.RawJSON).Result()
	if err != nil {
		return fmt.Errorf("작업 ACK 실패: %w", err)
	}

	// 2차 시도: 원본 JSON으로 매칭 실패 시, executionID로 검색하여 제거
	if removed == 0 {
		q.logger.Warnf("원본 JSON 매칭 실패, executionID로 검색 시도: executionID=%d", executionID)
		removed, err = q.removeByExecutionID(ctx, executionID)
		if err != nil {
			q.logger.Errorf("executionID로 제거 실패: executionID=%d, error=%v", executionID, err)
		}
	}

	// 타임스탬프 제거
	q.client.HDel(ctx, q.timestampHash, fmt.Sprintf("%d", executionID))

	if removed > 0 {
		q.logger.Infof("작업 완료 확인: executionID=%d", executionID)
	} else {
		q.logger.Warnf("작업이 processing 큐에 없음: executionID=%d", executionID)
	}

	return nil
}

// NackJob 작업 실패 - 다시 jobs 큐로 이동 (재시도)
// 원본 JSON을 우선 시도하고, 실패 시 executionID로 검색하여 제거
func (q *ReliableRedisQueue) NackJob(ctx context.Context, jobWrapper *JobWithRawJSON) error {
	executionID := jobWrapper.Job.ExecutionID

	// 1차 시도: 원본 JSON으로 제거
	removed, err := q.client.LRem(ctx, q.processingQueue, 1, jobWrapper.RawJSON).Result()
	if err != nil {
		return fmt.Errorf("processing 큐에서 제거 실패: %w", err)
	}

	// 2차 시도: 원본 JSON으로 매칭 실패 시, executionID로 검색하여 제거
	if removed == 0 {
		q.logger.Warnf("원본 JSON 매칭 실패, executionID로 검색 시도: executionID=%d", executionID)
		removed, err = q.removeByExecutionID(ctx, executionID)
		if err != nil {
			q.logger.Errorf("executionID로 제거 실패: executionID=%d, error=%v", executionID, err)
		}
	}

	// 타임스탬프 제거
	q.client.HDel(ctx, q.timestampHash, fmt.Sprintf("%d", executionID))

	// jobs 큐의 맨 앞으로 이동 (우선 처리)
	// 새로운 JSON으로 직렬화하여 등록 (일관된 형식 유지)
	newJSON, err := json.Marshal(jobWrapper.Job)
	if err != nil {
		return fmt.Errorf("작업 직렬화 실패: %w", err)
	}

	err = q.client.LPush(ctx, q.jobQueue, newJSON).Err()
	if err != nil {
		return fmt.Errorf("작업 NACK 실패: %w", err)
	}

	q.logger.Infof("작업 재시도 등록: executionID=%d, removed=%d", executionID, removed)
	return nil
}

// removeByExecutionID processing 큐에서 executionID로 작업을 찾아 제거
// JSON 필드 순서 차이로 인한 LREM 매칭 실패를 해결
func (q *ReliableRedisQueue) removeByExecutionID(ctx context.Context, executionID int64) (int64, error) {
	// processing 큐의 모든 작업 조회
	jobs, err := q.client.LRange(ctx, q.processingQueue, 0, -1).Result()
	if err != nil {
		return 0, fmt.Errorf("processing 큐 조회 실패: %w", err)
	}

	var removed int64 = 0
	for _, jobJSON := range jobs {
		var job models.TerraformJobPayload
		if err := json.Unmarshal([]byte(jobJSON), &job); err != nil {
			continue
		}

		if job.ExecutionID == executionID {
			// 실제 Redis에 있는 JSON 문자열로 제거 (정확한 매칭)
			count, err := q.client.LRem(ctx, q.processingQueue, 1, jobJSON).Result()
			if err != nil {
				q.logger.Errorf("LREM 실패: executionID=%d, error=%v", executionID, err)
				continue
			}
			removed += count
			q.logger.Infof("executionID로 작업 제거 성공: executionID=%d", executionID)
			break // 같은 executionID는 하나만 있어야 함
		}
	}

	return removed, nil
}

// ExecutionStatusChecker Backend API 상태 조회 인터페이스
type ExecutionStatusChecker interface {
	GetExecutionStatus(executionID int64, jobType models.JobType, tenantID string) (models.ExecutionStatus, error)
}

// RecoverStalledJobs 멈춘 작업 복구 (주기적 실행 필요)
// processing 큐에 오래 있는 작업을 Backend API 상태 확인 후 복구
// 개선사항:
// - 타임스탬프 없는 작업도 즉시 Backend API 확인 (기존: 10분 대기)
// - 중복 executionID 자동 제거
// - executionID 기반 제거로 JSON 매칭 실패 문제 해결
func (q *ReliableRedisQueue) RecoverStalledJobs(ctx context.Context, statusChecker ExecutionStatusChecker) (int, error) {
	// processing 큐의 모든 작업 확인
	jobs, err := q.client.LRange(ctx, q.processingQueue, 0, -1).Result()
	if err != nil {
		return 0, fmt.Errorf("processing 큐 조회 실패: %w", err)
	}

	if len(jobs) == 0 {
		return 0, nil
	}

	q.logger.Infof("Stalled Job Recovery 시작: processing 큐 작업 수=%d", len(jobs))

	recovered := 0
	removed := 0
	now := time.Now().Unix()

	// 중복 executionID 추적 (같은 ID가 여러 번 있으면 중복 제거)
	seenExecutionIDs := make(map[int64]bool)

	for _, jobJSON := range jobs {
		var job models.TerraformJobPayload
		if err := json.Unmarshal([]byte(jobJSON), &job); err != nil {
			q.logger.Warnf("파싱 불가능한 작업 제거: %s", jobJSON)
			q.client.LRem(ctx, q.processingQueue, 1, jobJSON)
			removed++
			continue
		}

		// 중복 executionID 처리 - 같은 ID가 여러 번 있으면 중복 제거
		if seenExecutionIDs[job.ExecutionID] {
			q.logger.Warnf("중복 작업 발견, 제거: executionID=%d", job.ExecutionID)
			q.client.LRem(ctx, q.processingQueue, 1, jobJSON)
			removed++
			continue
		}
		seenExecutionIDs[job.ExecutionID] = true

		// JobWithRawJSON 래퍼 생성 (원본 JSON 보존)
		jobWrapper := &JobWithRawJSON{
			Job:     &job,
			RawJSON: jobJSON,
		}

		// 작업 시작 시간 확인
		timestampStr, err := q.client.HGet(ctx, q.timestampHash, fmt.Sprintf("%d", job.ExecutionID)).Result()

		var elapsedMinutes int64 = 0
		noTimestamp := false

		if err != nil || timestampStr == "" {
			noTimestamp = true
			// 타임스탬프 기록 후 즉시 Backend API 확인 (기존: 다음 사이클 대기)
			q.client.HSet(ctx, q.timestampHash, fmt.Sprintf("%d", job.ExecutionID), now)
			q.logger.Warnf("타임스탬프 없는 작업 발견, Backend API 즉시 확인: executionID=%d", job.ExecutionID)
		} else {
			var timestamp int64
			fmt.Sscanf(timestampStr, "%d", &timestamp)
			elapsedMinutes = (now - timestamp) / 60

			// 10분 미만이면 정상 처리 중으로 간주 (타임스탬프가 있는 경우만)
			if elapsedMinutes < int64(q.stalledTimeout.Minutes()) {
				continue
			}
			q.logger.Warnf("Stalled Job 감지: executionID=%d, tenantID=%s, elapsed=%d분", job.ExecutionID, job.TenantID, elapsedMinutes)
		}

		// Backend API로 실제 상태 확인
		backendStatus, err := statusChecker.GetExecutionStatus(job.ExecutionID, job.JobType, job.TenantID)
		if err != nil {
			// 404/400 에러 - DB에 실행 레코드가 없음 (고아 작업)
			if strings.Contains(err.Error(), "status=400") || strings.Contains(err.Error(), "status=404") {
				q.logger.Warnf("DB에 존재하지 않는 고아 작업 제거: executionID=%d, error=%v", job.ExecutionID, err)
				q.AckJob(ctx, jobWrapper)
				removed++
				continue
			}
			// 그 외 에러 (네트워크 등) - 다음 복구 사이클에서 재시도
			q.logger.Errorf("Backend 상태 조회 실패 (다음 복구 사이클에서 재시도): executionID=%d, error=%v", job.ExecutionID, err)
			continue
		}

		// Backend 상태에 따라 복구 전략 결정
		switch backendStatus {
		case models.ExecutionStatusSuccess, models.ExecutionStatusFailed:
			// 이미 완료/실패 상태 - Worker가 Ack하지 못하고 종료된 경우
			q.logger.Infof("완료된 작업 제거: executionID=%d, backendStatus=%s", job.ExecutionID, backendStatus)
			q.AckJob(ctx, jobWrapper)
			removed++

		case models.ExecutionStatusQueued, models.ExecutionStatusRunning:
			// 타임스탬프가 없거나 10분 이상 경과 - Worker 크래시로 추정
			if noTimestamp || elapsedMinutes >= int64(q.stalledTimeout.Minutes()) {
				q.logger.Warnf("Stalled 작업 재시도 등록: executionID=%d, backendStatus=%s, noTimestamp=%v",
					job.ExecutionID, backendStatus, noTimestamp)
				q.NackJob(ctx, jobWrapper)
				recovered++
			}

		default:
			q.logger.Warnf("알 수 없는 Backend 상태: executionID=%d, status=%s", job.ExecutionID, backendStatus)
		}
	}

	q.logger.Infof("Stalled Job Recovery 완료: 재시도=%d, 제거=%d", recovered, removed)
	return recovered, nil
}

// GetProcessingCount processing 큐의 작업 수
func (q *ReliableRedisQueue) GetProcessingCount(ctx context.Context) (int64, error) {
	return q.client.LLen(ctx, q.processingQueue).Result()
}

// GetJobQueueLength job 큐의 작업 수
func (q *ReliableRedisQueue) GetJobQueueLength(ctx context.Context) (int64, error) {
	return q.client.LLen(ctx, q.jobQueue).Result()
}

// Close Redis 연결 종료
func (q *ReliableRedisQueue) Close() error {
	return q.client.Close()
}
