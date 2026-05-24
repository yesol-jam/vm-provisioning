package terraform

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/yesol-jam/vm-provisioning/internal/api"
	"github.com/yesol-jam/vm-provisioning/internal/config"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPasswordNotInCommandLine 비밀번호가 command line에 노출되지 않는지 확인
func TestPasswordNotInCommandLine(t *testing.T) {
	// Given: 테스트용 Executor 설정
	cfg := &config.Config{
		TerraformBinary:      "echo",  // echo 명령으로 대체 (실제 terraform 불필요)
		TerraformModulePath:  "/tmp/test-modules",
		WorkspacePath:        "/tmp/test-workspace",
		TerraformTimeout:     300,
		BackendAPIURL:        "http://localhost:8080",
		RedisHost:            "localhost",
		RedisPort:            "6379",
	}

	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel) // 테스트 중 로그 최소화

	apiClient := &api.Client{} // Mock client
	executor := NewExecutor(cfg, apiClient, logger)

	// When: Terraform 명령 실행 (비밀번호 포함)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	testPassword := "SuperSecret123!@#"
	args := []string{"apply"}

	// runTerraformWithOutput 직접 호출하여 환경변수 설정 확인
	output, _, err := executor.runTerraformWithOutput(ctx, "/tmp", 1, testPassword, args...)

	// Then: 성공적으로 실행되어야 함
	require.NoError(t, err)

	// 출력에 비밀번호가 포함되지 않아야 함
	assert.NotContains(t, output, testPassword,
		"비밀번호가 명령 출력에 노출되어서는 안 됩니다")
	assert.NotContains(t, output, fmt.Sprintf("-var=vsphere_password=%s", testPassword),
		"비밀번호가 command line argument로 전달되어서는 안 됩니다")
}

// TestPasswordInEnvironmentVariable 비밀번호가 환경변수로 전달되는지 확인
func TestPasswordInEnvironmentVariable(t *testing.T) {
	// Given: 환경변수를 출력하는 스크립트로 테스트
	cfg := &config.Config{
		TerraformBinary:      "/bin/sh",  // shell로 환경변수 출력
		TerraformModulePath:  "/tmp/test-modules",
		WorkspacePath:        "/tmp/test-workspace",
		TerraformTimeout:     300,
	}

	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	apiClient := &api.Client{}
	executor := NewExecutor(cfg, apiClient, logger)

	// When: 환경변수를 출력하는 명령 실행
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	testPassword := "TestPassword456"

	// sh -c "env | grep TF_VAR" 명령으로 환경변수 확인
	args := []string{"-c", "env | grep TF_VAR || true"}
	output, _, err := executor.runTerraformWithOutput(ctx, "/tmp", 1, testPassword, args...)

	// Then: 환경변수에 비밀번호가 설정되어야 함
	require.NoError(t, err)

	// TF_VAR_vsphere_password 환경변수가 존재해야 함
	assert.Contains(t, output, "TF_VAR_vsphere_password=",
		"비밀번호가 환경변수로 설정되어야 합니다")
	assert.Contains(t, output, testPassword,
		"환경변수에 비밀번호 값이 포함되어야 합니다")
}

// TestProcessListDoesNotContainPassword 실제 프로세스 목록에 비밀번호가 없는지 확인
func TestProcessListDoesNotContainPassword(t *testing.T) {
	if testing.Short() {
		t.Skip("긴 실행 시간이 필요한 테스트는 건너뜁니다")
	}

	// Given: 긴 실행 시간을 가진 명령
	cfg := &config.Config{
		TerraformBinary:      "sleep",  // sleep 명령으로 프로세스 유지
		TerraformModulePath:  "/tmp/test-modules",
		WorkspacePath:        "/tmp/test-workspace",
		TerraformTimeout:     300,
	}

	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	apiClient := &api.Client{}
	executor := NewExecutor(cfg, apiClient, logger)

	testPassword := "VerySecretPassword999"

	// When: 백그라운드에서 명령 실행
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resultChan := make(chan string)
	go func() {
		args := []string{"2"}  // 2초 sleep
		executor.runTerraformWithOutput(ctx, "/tmp", 1, testPassword, args...)
		resultChan <- "done"
	}()

	// 프로세스가 시작될 때까지 대기
	time.Sleep(100 * time.Millisecond)

	// Then: 프로세스 목록에서 비밀번호 검색
	cmd := exec.Command("ps", "aux")
	psOutput, err := cmd.CombinedOutput()
	require.NoError(t, err)

	processOutput := string(psOutput)

	// 프로세스 목록에 비밀번호가 노출되지 않아야 함
	assert.NotContains(t, processOutput, testPassword,
		"프로세스 목록(ps aux)에 비밀번호가 노출되어서는 안 됩니다")
	assert.NotContains(t, strings.ToLower(processOutput), strings.ToLower(testPassword),
		"프로세스 목록에 비밀번호가 대소문자 변환되어도 노출되어서는 안 됩니다")

	// 백그라운드 작업 완료 대기
	<-resultChan
}

// TestEmptyPasswordHandling 빈 비밀번호 처리 확인
func TestEmptyPasswordHandling(t *testing.T) {
	// Given
	cfg := &config.Config{
		TerraformBinary:      "/bin/sh",
		TerraformModulePath:  "/tmp/test-modules",
		WorkspacePath:        "/tmp/test-workspace",
		TerraformTimeout:     300,
	}

	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	apiClient := &api.Client{}
	executor := NewExecutor(cfg, apiClient, logger)

	// When: 빈 비밀번호로 실행
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	args := []string{"-c", "env | grep TF_VAR || true"}
	output, _, err := executor.runTerraformWithOutput(ctx, "/tmp", 1, "", args...)

	// Then: 환경변수가 설정되지 않아야 함
	require.NoError(t, err)
	assert.NotContains(t, output, "TF_VAR_vsphere_password",
		"빈 비밀번호인 경우 환경변수가 설정되지 않아야 합니다")
}
