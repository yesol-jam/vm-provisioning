package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/yesol-jam/vm-provisioning/pkg/models"
	"github.com/sirupsen/logrus"
)

const (
	apiKeyHeader   = "X-API-Key"
	tenantIDHeader = "X-Tenant-ID"

	// Internal API 경로 (TEST/TENANT 통합)
	internalAPIPath = "/api/internal/provisioning"
)

// Client Backend API 클라이언트
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	logger     *logrus.Logger
}

// NewClient 새 API 클라이언트 생성
func NewClient(baseURL, apiKey string, logger *logrus.Logger) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}
}

// getAPIPath Internal API 경로 반환 (TEST/TENANT 통합)
func (c *Client) getAPIPath(jobType models.JobType) string {
	// TEST와 TENANT 모두 동일한 Internal API 사용
	return internalAPIPath
}

// setHeaders 공통 헤더 설정 (API Key + Tenant ID)
func (c *Client) setHeaders(req *http.Request, tenantID string) {
	req.Header.Set(apiKeyHeader, c.apiKey)
	if tenantID != "" {
		req.Header.Set(tenantIDHeader, tenantID)
	}
}

// GetProjectDetail 프로젝트 상세 정보 조회 (JobType에 따라 다른 API 호출)
func (c *Client) GetProjectDetail(projectID int64, jobType models.JobType, tenantID string) (*models.TerraformProjectDetail, error) {
	apiPath := c.getAPIPath(jobType)
	url := fmt.Sprintf("%s%s/projects/%d", c.baseURL, apiPath, projectID)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("요청 생성 실패: %w", err)
	}
	c.setHeaders(req, tenantID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API 호출 실패: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API 응답 오류 (status=%d): %s", resp.StatusCode, string(body))
	}

	var apiResp struct {
		Status  string                        `json:"status"`
		Message string                        `json:"message"`
		Data    models.TerraformProjectDetail `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("응답 파싱 실패: %w", err)
	}

	return &apiResp.Data, nil
}

// UpdateExecutionStatus 실행 상태 업데이트 (JobType에 따라 다른 API 호출)
func (c *Client) UpdateExecutionStatus(executionID int64, jobType models.JobType, tenantID string, status models.ExecutionStatus, output, errorLog string, exitCode *int) error {
	apiPath := c.getAPIPath(jobType)
	apiURL := fmt.Sprintf("%s%s/executions/%d/status", c.baseURL, apiPath, executionID)

	// JSON 요청 본문 생성
	requestBody := map[string]interface{}{
		"status": status,
	}
	if output != "" {
		requestBody["output"] = truncateString(output, 50000)
	}
	if errorLog != "" {
		requestBody["errorLog"] = truncateString(errorLog, 10000)
	}
	if exitCode != nil {
		requestBody["exitCode"] = *exitCode
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("JSON 변환 실패: %w", err)
	}

	req, err := http.NewRequest(http.MethodPut, apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("요청 생성 실패: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req, tenantID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("API 호출 실패: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API 응답 오류 (status=%d): %s", resp.StatusCode, string(body))
	}

	c.logger.Infof("실행 상태 업데이트 완료: executionID=%d, status=%s", executionID, status)
	return nil
}

// AppendExecutionOutput 실행 출력 추가 (스트리밍용)
func (c *Client) AppendExecutionOutput(executionID int64, jobType models.JobType, tenantID string, outputChunk string) error {
	apiPath := c.getAPIPath(jobType)
	url := fmt.Sprintf("%s%s/executions/%d/output", c.baseURL, apiPath, executionID)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(outputChunk))
	if err != nil {
		return fmt.Errorf("요청 생성 실패: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain")
	c.setHeaders(req, tenantID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("API 호출 실패: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API 응답 오류 (status=%d): %s", resp.StatusCode, string(body))
	}

	return nil
}

// AddInstanceVm 인스턴스 VM 추가 (JobType에 따라 다른 API 호출)
func (c *Client) AddInstanceVm(projectID int64, jobType models.JobType, tenantID string, request map[string]interface{}) error {
	apiPath := c.getAPIPath(jobType)
	url := fmt.Sprintf("%s%s/projects/%d/vms", c.baseURL, apiPath, projectID)

	jsonData, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("JSON 변환 실패: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("요청 생성 실패: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req, tenantID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("API 호출 실패: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API 응답 오류 (status=%d): %s", resp.StatusCode, string(body))
	}

	c.logger.Infof("InstanceVm 등록 완료: projectID=%d", projectID)
	return nil
}

// GetExecutionStatus 실행 상태 조회 (JobType에 따라 다른 API 호출)
func (c *Client) GetExecutionStatus(executionID int64, jobType models.JobType, tenantID string) (models.ExecutionStatus, error) {
	apiPath := c.getAPIPath(jobType)
	url := fmt.Sprintf("%s%s/executions/%d/status", c.baseURL, apiPath, executionID)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("요청 생성 실패: %w", err)
	}
	c.setHeaders(req, tenantID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("API 호출 실패: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API 응답 오류 (status=%d): %s", resp.StatusCode, string(body))
	}

	var apiResp struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Data    struct {
			Status models.ExecutionStatus `json:"status"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return "", fmt.Errorf("응답 파싱 실패: %w", err)
	}

	return apiResp.Data.Status, nil
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "... (truncated)"
}
