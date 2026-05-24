package api

import (
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/yesol-jam/vm-provisioning/pkg/models"
	"github.com/sirupsen/logrus"
)

// RetryableClient Retry 로직이 포함된 API 클라이언트
type RetryableClient struct {
	client     *Client
	maxRetries int
	baseDelay  time.Duration
	maxDelay   time.Duration
	logger     *logrus.Logger
}

// NewRetryableClient 새 Retry 가능한 API 클라이언트 생성
func NewRetryableClient(baseURL, apiKey string, logger *logrus.Logger) *RetryableClient {
	return &RetryableClient{
		client:     NewClient(baseURL, apiKey, logger),
		maxRetries: 3,
		baseDelay:  1 * time.Second,
		maxDelay:   30 * time.Second,
		logger:     logger,
	}
}

// GetProjectDetailWithRetry 프로젝트 상세 조회 (재시도 포함)
func (c *RetryableClient) GetProjectDetailWithRetry(projectID int64, jobType models.JobType, tenantID string) (*models.TerraformProjectDetail, error) {
	var lastErr error

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		detail, err := c.client.GetProjectDetail(projectID, jobType, tenantID)

		if err == nil {
			if attempt > 0 {
				c.logger.Infof("프로젝트 조회 성공 (재시도 %d회): projectID=%d", attempt, projectID)
			}
			return detail, nil
		}

		lastErr = err

		// 클라이언트 에러(4xx)는 재시도하지 않음
		if isClientError(err) {
			c.logger.Warnf("클라이언트 에러 - 재시도 중단: %v", err)
			return nil, err
		}

		// 마지막 시도가 아니면 대기 후 재시도
		if attempt < c.maxRetries {
			delay := c.calculateBackoff(attempt)
			c.logger.Warnf("프로젝트 조회 실패 - 재시도 %d/%d (대기: %v): %v",
				attempt+1, c.maxRetries, delay, err)
			time.Sleep(delay)
		}
	}

	return nil, fmt.Errorf("최대 재시도 횟수 초과 (%d회): %w", c.maxRetries, lastErr)
}

// UpdateExecutionStatusWithRetry 실행 상태 업데이트 (재시도 포함)
func (c *RetryableClient) UpdateExecutionStatusWithRetry(
	executionID int64,
	jobType models.JobType,
	tenantID string,
	status models.ExecutionStatus,
	output, errorLog string,
	exitCode *int,
) error {
	var lastErr error

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		err := c.client.UpdateExecutionStatus(executionID, jobType, tenantID, status, output, errorLog, exitCode)

		if err == nil {
			if attempt > 0 {
				c.logger.Infof("상태 업데이트 성공 (재시도 %d회): executionID=%d, status=%s",
					attempt, executionID, status)
			}
			return nil
		}

		lastErr = err

		// 클라이언트 에러(4xx)는 재시도하지 않음
		if isClientError(err) {
			c.logger.Warnf("클라이언트 에러 - 재시도 중단: %v", err)
			return err
		}

		// 마지막 시도가 아니면 대기 후 재시도
		if attempt < c.maxRetries {
			delay := c.calculateBackoff(attempt)
			c.logger.Warnf("상태 업데이트 실패 - 재시도 %d/%d (대기: %v): executionID=%d, error=%v",
				attempt+1, c.maxRetries, delay, executionID, err)
			time.Sleep(delay)
		}
	}

	return fmt.Errorf("최대 재시도 횟수 초과 (%d회): %w", c.maxRetries, lastErr)
}

// AppendExecutionOutputWithRetry 실행 출력 추가 (재시도 포함)
func (c *RetryableClient) AppendExecutionOutputWithRetry(executionID int64, jobType models.JobType, tenantID string, outputChunk string) error {
	var lastErr error

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		err := c.client.AppendExecutionOutput(executionID, jobType, tenantID, outputChunk)

		if err == nil {
			return nil
		}

		lastErr = err

		// 클라이언트 에러는 재시도하지 않음
		if isClientError(err) {
			return err
		}

		// 마지막 시도가 아니면 대기 후 재시도
		if attempt < c.maxRetries {
			delay := c.calculateBackoff(attempt)
			time.Sleep(delay)
		}
	}

	return fmt.Errorf("최대 재시도 횟수 초과 (%d회): %w", c.maxRetries, lastErr)
}

// AddInstanceVm 인스턴스 VM 추가 (재시도 포함)
func (c *RetryableClient) AddInstanceVm(projectID int64, jobType models.JobType, tenantID string, request map[string]interface{}) error {
	var lastErr error

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		err := c.client.AddInstanceVm(projectID, jobType, tenantID, request)

		if err == nil {
			if attempt > 0 {
				c.logger.Infof("InstanceVm 등록 성공 (재시도 %d회): projectID=%d", attempt, projectID)
			}
			return nil
		}

		lastErr = err

		// 클라이언트 에러(4xx)는 재시도하지 않음
		if isClientError(err) {
			c.logger.Warnf("클라이언트 에러 - 재시도 중단: %v", err)
			return err
		}

		// 마지막 시도가 아니면 대기 후 재시도
		if attempt < c.maxRetries {
			delay := c.calculateBackoff(attempt)
			c.logger.Warnf("InstanceVm 등록 실패 - 재시도 %d/%d (대기: %v): %v",
				attempt+1, c.maxRetries, delay, err)
			time.Sleep(delay)
		}
	}

	return fmt.Errorf("최대 재시도 횟수 초과 (%d회): %w", c.maxRetries, lastErr)
}

// calculateBackoff Exponential Backoff 계산
func (c *RetryableClient) calculateBackoff(attempt int) time.Duration {
	// Exponential backoff: baseDelay * 2^attempt
	delay := c.baseDelay * time.Duration(math.Pow(2, float64(attempt)))

	// 최대 지연 시간 제한
	if delay > c.maxDelay {
		delay = c.maxDelay
	}

	// Jitter 추가 (±20%)
	jitter := time.Duration(float64(delay) * 0.2 * (2*float64(time.Now().UnixNano()%100)/100 - 1))
	delay += jitter

	return delay
}

// isClientError 클라이언트 에러(4xx) 확인
func isClientError(err error) bool {
	if err == nil {
		return false
	}

	// 에러 메시지에서 4xx 상태 코드 확인
	errStr := err.Error()
	for status := http.StatusBadRequest; status < http.StatusInternalServerError; status++ {
		statusStr := fmt.Sprintf("status=%d", status)
		if contains(errStr, statusStr) {
			return true
		}
	}

	return false
}

// contains 문자열 포함 확인 헬퍼
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
