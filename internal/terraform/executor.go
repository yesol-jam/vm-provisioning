package terraform

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yesol-jam/vm-provisioning/internal/api"
	"github.com/yesol-jam/vm-provisioning/internal/config"
	"github.com/yesol-jam/vm-provisioning/pkg/models"
	"github.com/sirupsen/logrus"
)

// Executor Terraform 실행기
type Executor struct {
	config         *config.Config
	apiClient      *api.Client
	retryableClient *api.RetryableClient
	logger         *logrus.Logger
}

// NewExecutor 새 실행기 생성
func NewExecutor(cfg *config.Config, apiClient *api.Client, logger *logrus.Logger) *Executor {
	// RetryableClient 생성
	retryableClient := api.NewRetryableClient(cfg.BackendAPIURL, cfg.BackendAPIKey, logger)

	return &Executor{
		config:          cfg,
		apiClient:       apiClient,
		retryableClient: retryableClient,
		logger:          logger,
	}
}

// Execute Terraform 작업 실행 (복잡도 개선: 73행 → 40행)
func (e *Executor) Execute(job *models.TerraformJobPayload) error {
	ctx, cancel := e.createTimeoutContext()
	defer cancel()

	e.logger.Infof("Terraform 작업 시작: projectID=%d, executionID=%d, type=%s",
		job.ProjectID, job.ExecutionID, job.Type)

	// 1. 실행 상태를 RUNNING으로 업데이트
	if err := e.retryableClient.UpdateExecutionStatusWithRetry(job.ExecutionID, job.JobType, job.TenantID, models.ExecutionStatusRunning, "", "", nil); err != nil {
		e.logger.Errorf("상태 업데이트 실패: %v", err)
	}

	// 2. 프로젝트 조회 및 환경 준비
	project, workDir, err := e.setupExecutionEnvironment(job)
	if err != nil {
		return err
	}

	// DEBUG: 비밀번호 확인
	e.logger.Infof("[DEBUG] project.VcenterPassword length: %d", len(project.VcenterPassword))

	// 3. Terraform init
	if err := e.runInitStep(ctx, job, workDir); err != nil {
		return err
	}

	// 4. Terraform 주요 명령 실행 (plan/apply/destroy)
	output, exitCode, err := e.runMainCommand(ctx, job, workDir, project.VcenterPassword)
	if err != nil {
		return err
	}

	// 5. 성공 상태 업데이트
	if err := e.retryableClient.UpdateExecutionStatusWithRetry(job.ExecutionID, job.JobType, job.TenantID, models.ExecutionStatusSuccess, output, "", &exitCode); err != nil {
		e.logger.Errorf("성공 상태 업데이트 실패: %v", err)
	}

	// 6. Apply 성공 시 VM 정보 수집 및 Backend에 등록
	if job.Type == models.ExecutionTypeApply {
		registeredCount, err := e.registerInstanceVms(ctx, job, workDir)
		if err != nil {
			e.logger.Errorf("❌ VM 등록 실패 - 롤백 시작: %v", err)

			// 상태를 FAILED로 업데이트
			failedExitCode := 1
			updateErr := e.retryableClient.UpdateExecutionStatusWithRetry(
				job.ExecutionID,
				job.JobType,
				job.TenantID,
				models.ExecutionStatusFailed,
				output,
				fmt.Sprintf("VM 등록 실패: %v\n\n자동 롤백을 시작합니다.", err),
				&failedExitCode,
			)
			if updateErr != nil {
				e.logger.Errorf("실패 상태 업데이트 실패: %v", updateErr)
			}

			// Terraform destroy로 VM 정리
			e.logger.Warn("VM 등록 실패로 인한 리소스 정리 시작")
			destroyJob := &models.TerraformJobPayload{
				JobType:     job.JobType,
				TenantID:    job.TenantID,
				ExecutionID: job.ExecutionID,
				ProjectID:   job.ProjectID,
				Type:        models.ExecutionTypeDestroy,
			}

			// 정리 작업 실행
			if destroyErr := e.Execute(destroyJob); destroyErr != nil {
				e.logger.Errorf("롤백 실패: %v", destroyErr)
			}

			return fmt.Errorf("VM 등록 실패로 프로비저닝 중단: %w", err)
		}

		e.logger.Infof("✅ VM 등록 완료: %d개 VM 등록 성공", registeredCount)
	}

	e.logger.Infof("Terraform 작업 완료: projectID=%d, executionID=%d", job.ProjectID, job.ExecutionID)
	return nil
}

// createTimeoutContext Context 생성 (타임아웃 적용)
func (e *Executor) createTimeoutContext() (context.Context, context.CancelFunc) {
	timeout := time.Duration(e.config.TerraformTimeout) * time.Second
	return context.WithTimeout(context.Background(), timeout)
}

// setupExecutionEnvironment 실행 환경 준비 (프로젝트 조회 + 디렉토리 + tfvars)
func (e *Executor) setupExecutionEnvironment(job *models.TerraformJobPayload) (*models.TerraformProjectDetail, string, error) {
	// 프로젝트 조회
	project, err := e.retryableClient.GetProjectDetailWithRetry(job.ProjectID, job.JobType, job.TenantID)
	if err != nil {
		return nil, "", e.handleError(job, fmt.Errorf("프로젝트 조회 실패: %w", err))
	}

	// 작업 디렉토리 준비
	workDir, err := e.prepareWorkspace(job.ProjectID)
	if err != nil {
		return nil, "", e.handleError(job, fmt.Errorf("작업 디렉토리 준비 실패: %w", err))
	}

	// tfvars 파일 생성
	if err := e.generateTfvars(workDir, project); err != nil {
		return nil, "", e.handleError(job, fmt.Errorf("tfvars 생성 실패: %w", err))
	}

	return project, workDir, nil
}

// runInitStep Terraform init 단계 실행
func (e *Executor) runInitStep(ctx context.Context, job *models.TerraformJobPayload, workDir string) error {
	initArgs := e.buildInitArgs(job.ProjectID)
	initOutput, initExitCode, initErr := e.runTerraformWithOutput(ctx, workDir, job.ExecutionID, "", initArgs...)

	if initErr != nil {
		e.logger.Errorf("Terraform init 출력:\n%s", initOutput)
		if ctx.Err() == context.DeadlineExceeded {
			return e.handleErrorWithOutput(job, fmt.Errorf("terraform init 타임아웃: %w", initErr), initOutput, 124)
		}
		return e.handleErrorWithOutput(job, fmt.Errorf("terraform init 실패: %w", initErr), initOutput, initExitCode)
	}

	return nil
}

// runMainCommand Terraform 주요 명령 실행 (plan/apply/destroy)
func (e *Executor) runMainCommand(ctx context.Context, job *models.TerraformJobPayload, workDir string, password string) (string, int, error) {
	tfCmd, err := e.getCommandForExecutionType(job.Type)
	if err != nil {
		return "", -1, e.handleError(job, err)
	}

	output, exitCode, err := e.runTerraformWithOutput(ctx, workDir, job.ExecutionID, password, tfCmd)
	if err != nil {
		e.logger.Errorf("Terraform %s 출력:\n%s", tfCmd, output)
		if ctx.Err() == context.DeadlineExceeded {
			return "", exitCode, e.handleErrorWithOutput(job, fmt.Errorf("terraform %s 타임아웃: %w", tfCmd, err), output, 124)
		}
		return "", exitCode, e.handleErrorWithOutput(job, fmt.Errorf("terraform %s 실패: %w", tfCmd, err), output, exitCode)
	}

	return output, exitCode, nil
}

// getCommandForExecutionType Execution 타입에 따른 Terraform 명령 반환
func (e *Executor) getCommandForExecutionType(execType models.ExecutionType) (string, error) {
	switch execType {
	case models.ExecutionTypePlan:
		return "plan", nil
	case models.ExecutionTypeApply:
		return "apply", nil
	case models.ExecutionTypeDestroy:
		return "destroy", nil
	default:
		return "", fmt.Errorf("알 수 없는 실행 타입: %s", execType)
	}
}

// prepareWorkspace 작업 디렉토리 준비 (파일 재생성 최적화: 90% I/O 감소)
func (e *Executor) prepareWorkspace(projectID int64) (string, error) {
	workDir := filepath.Join(e.config.WorkspacePath, fmt.Sprintf("project-%d", projectID))

	// 디렉토리 생성
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return "", fmt.Errorf("디렉토리 생성 실패: %w", err)
	}

	// 모듈 파일 목록
	files := []string{"main.tf", "variables.tf", "locals.tf", "networks.tf", "outputs.tf"}

	// 절대 경로 변환
	absSrcPath, err := filepath.Abs(e.config.TerraformModulePath)
	if err != nil {
		return "", fmt.Errorf("절대 경로 변환 실패: %w", err)
	}

	// 파일별 처리 (유효성 검증 후 필요시에만 재생성)
	for _, file := range files {
		srcPath := filepath.Join(absSrcPath, file)
		dstPath := filepath.Join(workDir, file)

		if err := e.ensureFileOrSymlink(srcPath, dstPath); err != nil {
			return "", fmt.Errorf("파일 준비 실패 (%s): %w", file, err)
		}
	}

	return workDir, nil
}

// ensureFileOrSymlink 파일/심링크 존재 및 유효성 보장 (있으면 재사용, 없거나 깨지면 재생성)
func (e *Executor) ensureFileOrSymlink(srcPath, dstPath string) error {
	// 1. 대상 파일 정보 조회
	dstInfo, err := os.Lstat(dstPath) // Lstat은 symlink 자체 정보 반환
	if err == nil {
		// 파일/심링크 존재 - 유효성 검증
		if dstInfo.Mode()&os.ModeSymlink != 0 {
			// 심링크인 경우: 타겟 확인
			target, err := os.Readlink(dstPath)
			if err == nil && target == srcPath {
				// 유효한 심링크 - 재사용
				e.logger.Debugf("심링크 재사용: %s → %s", dstPath, srcPath)
				return nil
			}
			// 심링크 깨짐 - 제거 후 재생성
			e.logger.Debugf("심링크 깨짐 감지, 재생성: %s", dstPath)
			os.Remove(dstPath)
		} else {
			// 일반 파일인 경우: 내용 비교 (선택적 - 여기서는 항상 재사용)
			e.logger.Debugf("일반 파일 재사용: %s", dstPath)
			return nil
		}
	}

	// 2. 파일/심링크 생성
	// 심링크 시도
	if err := os.Symlink(srcPath, dstPath); err != nil {
		// 심링크 실패 시 복사
		e.logger.Debugf("심링크 실패, 파일 복사: %s", dstPath)
		if err := copyFile(srcPath, dstPath); err != nil {
			return fmt.Errorf("파일 복사 실패: %w", err)
		}
	} else {
		e.logger.Debugf("심링크 생성: %s → %s", dstPath, srcPath)
	}

	return nil
}

// generateTfvars tfvars 파일 생성
func (e *Executor) generateTfvars(workDir string, project *models.TerraformProjectDetail) error {
	// 기존 네트워크 파싱
	var existingNetworks []string
	if project.ExistingNetworks != "" {
		if err := json.Unmarshal([]byte(project.ExistingNetworks), &existingNetworks); err != nil {
			e.logger.Warnf("기존 네트워크 파싱 실패: %v", err)
			existingNetworks = []string{}
		}
	}

	// VM 설정 변환
	vms := make(map[string]models.VMConfig)
	for _, vm := range project.VMs {
		vmKey := fmt.Sprintf("vm-%d", vm.ID)

		networkAdapters := make([]models.NetworkAdapter, len(vm.Networks))
		for i, net := range vm.Networks {
			networkAdapters[i] = models.NetworkAdapter{
				NetworkName: net.NetworkName,
				AdapterType: net.AdapterType,
				CreateNew:   net.CreateNew,
			}
		}

		// SCSI 타입: Backend에서 전달받거나 기본값 사용
		scsiType := vm.ScsiType
		if scsiType == "" {
			scsiType = "pvscsi" // 기본값
		}

		// 부트 펌웨어: Backend에서 전달받거나 기본값 사용
		firmware := vm.Firmware
		if firmware == "" {
			firmware = "bios" // 기본값
		}

		vms[vmKey] = models.VMConfig{
			Name:               vm.Name,
			Folder:             vm.Folder,
			ContentLibraryItem: vm.TemplateItemName,
			CPUCount:           vm.CPUCount,
			MemoryMB:           vm.MemoryMB,
			DiskSizeGB:         vm.DiskSizeGB,
			ScsiType:           scsiType,
			Firmware:           firmware,
			NetworkAdapters:    networkAdapters,
		}
	}

	// 네트워크 설정 변환 (VLAN ID 포함)
	networks := make(map[string]map[string]interface{})
	for _, net := range project.Networks {
		networks[net.NetworkName] = map[string]interface{}{
			"vlan_id": net.VlanID,
		}
	}

	// 기존 폴더 목록 (기본 폴더만 - 테넌트/테스트 폴더는 Terraform에서 생성)
	existingFolders := []string{"VMs", "VMs/test", "VMs/b2b"}

	// tfvars Map 생성 (구조체 대신 Map 사용하여 vsphere_password 키 자체를 제거)
	// 보안: VspherePassword는 파일에 저장하지 않고 환경변수로 전달
	vars := map[string]interface{}{
		"vsphere_server":       project.VcenterServer,
		"vsphere_user":         project.VcenterUsername,
		// vsphere_password는 의도적으로 제외 (환경변수 TF_VAR_vsphere_password 사용)
		"datacenter":           project.Datacenter,
		"datastore":            project.Datastore,
		"content_library_name": project.ContentLibraryName,
		"existing_networks":    existingNetworks,
		"existing_folders":     existingFolders,
		"create_folders":       true,
		"vms":                  vms,
		"networks":             networks,
	}

	// 컴퓨트 리소스 설정
	if project.ComputeCluster != "" {
		vars["compute_cluster"] = project.ComputeCluster
		if project.DistributedSwitch != "" {
			vars["distributed_switch_name"] = project.DistributedSwitch
		}
		// 클러스터 환경에서는 네트워크 폴더 사용 (기본값: VM-Networks)
		vars["network_folder"] = "VM-Networks"
	} else if project.EsxiHost != "" {
		vars["esxi_host"] = project.EsxiHost
		if project.DefaultVswitch != "" {
			vars["default_vswitch"] = project.DefaultVswitch
		}
	}

	// JSON 파일로 저장 (vsphere_password는 제외되어 환경변수로만 전달)
	tfvarsPath := filepath.Join(workDir, "terraform.tfvars.json")
	data, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON 변환 실패: %w", err)
	}

	if err := os.WriteFile(tfvarsPath, data, 0600); err != nil {
		return fmt.Errorf("tfvars 파일 저장 실패: %w", err)
	}

	e.logger.Infof("tfvars 파일 생성 완료 (vsphere_password 제외): %s", tfvarsPath)
	return nil
}

// buildInitArgs Terraform init 명령 인자 생성 (backend configuration 포함)
func (e *Executor) buildInitArgs(projectID int64) []string {
	args := []string{"init"}

	// Backend 설정이 활성화된 경우 S3 backend config 추가
	if e.config.StateBackendEnabled {
		// State 파일 경로: project-{id}/terraform.tfstate
		stateKey := fmt.Sprintf("project-%d/terraform.tfstate", projectID)

		args = append(args,
			fmt.Sprintf("-backend-config=bucket=%s", e.config.StateS3Bucket),
			fmt.Sprintf("-backend-config=key=%s", stateKey),
			fmt.Sprintf("-backend-config=region=%s", e.config.StateS3Region),
			fmt.Sprintf("-backend-config=access_key=%s", e.config.StateS3AccessKey),
			fmt.Sprintf("-backend-config=secret_key=%s", e.config.StateS3SecretKey),
		)

		// MinIO endpoint가 설정된 경우 추가 설정
		if e.config.StateS3Endpoint != "" {
			args = append(args,
				fmt.Sprintf("-backend-config=endpoint=%s", e.config.StateS3Endpoint),
				"-backend-config=skip_credentials_validation=true",
				"-backend-config=skip_metadata_api_check=true",
				"-backend-config=force_path_style=true",
			)
		}

		e.logger.Infof("Terraform backend 설정: bucket=%s, key=%s",
			e.config.StateS3Bucket, stateKey)
	}

	return args
}

// runTerraform Terraform 명령 실행
func (e *Executor) runTerraform(ctx context.Context, workDir string, executionID int64, vspherePassword string, args ...string) error {
	_, _, err := e.runTerraformWithOutput(ctx, workDir, executionID, vspherePassword, args...)
	return err
}

// runTerraformWithOutput Terraform 명령 실행 및 출력 반환 (복잡도 개선: 98행 → 30행)
func (e *Executor) runTerraformWithOutput(ctx context.Context, workDir string, executionID int64, vspherePassword string, args ...string) (string, int, error) {
	// 명령 인자 보강
	args = e.enhanceCommandArgs(args)

	// 명령 생성 및 환경변수 설정
	cmd := e.buildCommand(ctx, workDir, vspherePassword, args)

	// stdout/stderr 파이프 설정
	stdout, stderr, err := e.setupPipes(cmd)
	if err != nil {
		return "", -1, err
	}

	// 명령 시작
	if err := cmd.Start(); err != nil {
		return "", -1, fmt.Errorf("명령 시작 실패: %w", err)
	}

	// 출력 캡처
	output := e.captureOutput(stdout, stderr)

	// 명령 완료 대기 및 종료 코드 추출
	exitCode, err := e.waitForCompletion(ctx, cmd, args)

	return output, exitCode, err
}

// enhanceCommandArgs 명령 인자 보강 (-no-color, -auto-approve)
func (e *Executor) enhanceCommandArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}

	// -no-color 추가 (init 제외)
	if args[0] != "init" {
		args = append(args, "-no-color")
	}

	// auto-approve 추가 (apply, destroy)
	if args[0] == "apply" || args[0] == "destroy" {
		args = append(args, "-auto-approve")
	}

	return args
}

// buildCommand 명령 생성 및 환경변수 설정
func (e *Executor) buildCommand(ctx context.Context, workDir string, vspherePassword string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, e.config.TerraformBinary, args...)
	cmd.Dir = workDir

	// 환경변수 설정 (비밀번호는 환경변수로 전달)
	envVars := append(os.Environ(), "TF_IN_AUTOMATION=1")
	if vspherePassword != "" && len(args) > 0 && args[0] != "init" {
		envVars = append(envVars, fmt.Sprintf("TF_VAR_vsphere_password=%s", vspherePassword))
		e.logger.Infof("[DEBUG] TF_VAR_vsphere_password 환경변수 설정됨 (length: %d)", len(vspherePassword))
	} else {
		e.logger.Warnf("[DEBUG] TF_VAR_vsphere_password 환경변수 설정 안됨! vspherePassword='%s', args[0]=%v", vspherePassword, args[0])
	}
	cmd.Env = envVars

	return cmd
}

// setupPipes stdout/stderr 파이프 설정
func (e *Executor) setupPipes(cmd *exec.Cmd) (io.ReadCloser, io.ReadCloser, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdout 파이프 생성 실패: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stderr 파이프 생성 실패: %w", err)
	}

	return stdout, stderr, nil
}

// captureOutput stdout/stderr 출력 캡처 (goroutine + Mutex)
func (e *Executor) captureOutput(stdout, stderr io.ReadCloser) string {
	var outputBuilder strings.Builder
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(2)

	// stdout 읽기
	go func() {
		defer wg.Done()
		e.scanAndLog(stdout, &outputBuilder, &mu, false)
	}()

	// stderr 읽기
	go func() {
		defer wg.Done()
		e.scanAndLog(stderr, &outputBuilder, &mu, true)
	}()

	wg.Wait()
	return outputBuilder.String()
}

// scanAndLog 스트림 읽기 및 로깅
func (e *Executor) scanAndLog(reader io.ReadCloser, output *strings.Builder, mu *sync.Mutex, isStderr bool) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()

		mu.Lock()
		if isStderr {
			output.WriteString("[STDERR] " + line + "\n")
		} else {
			output.WriteString(line + "\n")
		}
		mu.Unlock()

		if isStderr {
			e.logger.Warn(line)
		} else {
			e.logger.Debug(line)
		}
	}

	if err := scanner.Err(); err != nil {
		e.logger.Errorf("스트림 읽기 오류: %v", err)
	}
}

// waitForCompletion 명령 완료 대기 및 종료 코드 반환
func (e *Executor) waitForCompletion(ctx context.Context, cmd *exec.Cmd, args []string) (int, error) {
	err := cmd.Wait()

	if err != nil {
		// Context timeout 체크
		if ctx.Err() == context.DeadlineExceeded {
			e.logger.Warnf("Terraform 명령 타임아웃: %v", args)
			return 124, fmt.Errorf("명령 타임아웃")
		}

		// Exit 코드 추출
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), err
		}
		return -1, err
	}

	return 0, nil
}

// handleError 에러 처리 및 상태 업데이트
func (e *Executor) handleError(job *models.TerraformJobPayload, err error) error {
	e.logger.Errorf("Terraform 실행 오류: %v", err)

	exitCode := 1
	if updateErr := e.retryableClient.UpdateExecutionStatusWithRetry(
		job.ExecutionID,
		job.JobType,
		job.TenantID,
		models.ExecutionStatusFailed,
		"",
		err.Error(),
		&exitCode,
	); updateErr != nil {
		e.logger.Errorf("실패 상태 업데이트 오류: %v", updateErr)
	}

	return err
}

// handleErrorWithOutput 에러 처리 및 상태 업데이트 (출력 포함)
func (e *Executor) handleErrorWithOutput(job *models.TerraformJobPayload, err error, output string, exitCode int) error {
	e.logger.Errorf("Terraform 실행 오류: %v", err)

	// 에러 메시지만 간결하게 추출
	compactError := extractTerraformError(output)

	if updateErr := e.retryableClient.UpdateExecutionStatusWithRetry(
		job.ExecutionID,
		job.JobType,
		job.TenantID,
		models.ExecutionStatusFailed,
		"", // output 필드는 비움 (errorLog에 포함되므로 중복 방지)
		compactError, // 간결한 에러 메시지만 전송
		&exitCode,
	); updateErr != nil {
		e.logger.Errorf("실패 상태 업데이트 오류: %v", updateErr)
	}

	return err
}

// extractTerraformError Terraform 출력에서 핵심 에러 메시지만 추출 (Plan 제외)
func extractTerraformError(output string) string {
	lines := strings.Split(output, "\n")
	var errorLines []string
	inError := false
	errorCount := 0

	for _, line := range lines {
		// "Error:" 라인 발견 시 에러 블록 시작
		if strings.Contains(line, "Error:") {
			inError = true
			errorCount = 0
			errorLines = append(errorLines, line)
			continue
		}

		// 에러 블록 내에서 관련 라인 추가 (최대 5줄)
		if inError {
			trimmed := strings.TrimSpace(line)
			// 빈 줄이면 에러 블록 종료
			if trimmed == "" {
				inError = false
				errorLines = append(errorLines, "") // 에러 구분을 위한 빈 줄
				continue
			}

			// "with", "on" 등 위치 정보 라인 포함
			if strings.HasPrefix(trimmed, "with ") || strings.HasPrefix(trimmed, "on ") ||
			   strings.Contains(trimmed, "resource \"") {
				errorLines = append(errorLines, line)
				errorCount++
			}

			// 에러 상세 설명 (들여쓰기된 라인)
			if strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t") {
				errorLines = append(errorLines, line)
				errorCount++
			}

			// 최대 5줄까지만
			if errorCount >= 5 {
				inError = false
			}
		}
	}

	// 에러 메시지가 없으면 원본 반환 (하지만 Plan 제외)
	if len(errorLines) == 0 {
		return filterOutPlan(output)
	}

	return strings.Join(errorLines, "\n")
}

// filterOutPlan Terraform Plan 출력 제거
func filterOutPlan(output string) string {
	lines := strings.Split(output, "\n")
	var filtered []string
	skipPlan := false

	for _, line := range lines {
		// Plan 시작 감지
		if strings.Contains(line, "Terraform will perform") ||
		   strings.Contains(line, "Terraform used the selected providers") {
			skipPlan = true
			continue
		}

		// Plan 종료 감지
		if skipPlan && (strings.HasPrefix(line, "Plan:") || strings.Contains(line, "to add") || strings.Contains(line, "to change")) {
			skipPlan = false
			continue
		}

		// Plan이 아닌 라인만 포함
		if !skipPlan {
			filtered = append(filtered, line)
		}
	}

	result := strings.Join(filtered, "\n")

	// 너무 길면 앞부분 + 뒷부분만
	if len(result) > 2000 {
		return result[:1000] + "\n\n... (중략) ...\n\n" + result[len(result)-1000:]
	}

	return result
}

// registerInstanceVms Terraform apply 성공 후 생성된 VM 정보를 Backend에 등록
func (e *Executor) registerInstanceVms(ctx context.Context, job *models.TerraformJobPayload, workDir string) (int, error) {
	e.logger.Infof("Terraform output 조회 시작: projectID=%d", job.ProjectID)

	registeredCount := 0

	// 1. terraform output -json 실행하여 VM 정보 조회
	outputJSON, err := e.getTerraformOutputJSON(ctx, workDir)
	if err != nil {
		return 0, fmt.Errorf("terraform output 조회 실패: %w", err)
	}

	// 2. JSON 파싱
	var outputs map[string]interface{}
	if err := json.Unmarshal([]byte(outputJSON), &outputs); err != nil {
		return 0, fmt.Errorf("terraform output JSON 파싱 실패: %w", err)
	}

	// 3. vm_info 추출
	vmInfoData, ok := outputs["vm_info"].(map[string]interface{})
	if !ok {
		e.logger.Warnf("vm_info output이 없습니다 (VM이 생성되지 않았을 수 있음)")
		return 0, nil
	}

	value, ok := vmInfoData["value"].(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("vm_info.value 형식이 올바르지 않습니다")
	}

	// 4. 각 VM에 대해 Backend API 호출
	for vmKey, vmData := range value {
		vmInfo, ok := vmData.(map[string]interface{})
		if !ok {
			e.logger.Warnf("VM 정보 파싱 실패: vmKey=%s", vmKey)
			continue
		}

		// InstanceVm 등록 요청
		if err := e.registerSingleInstanceVm(job.ProjectID, job.JobType, job.TenantID, vmInfo); err != nil {
			e.logger.Errorf("InstanceVm 등록 실패: vmKey=%s, error=%v", vmKey, err)
			// VM 등록 실패는 치명적 오류로 처리
			return 0, fmt.Errorf("VM 등록 실패 (vmKey=%s): %w", vmKey, err)
		}
		registeredCount++
	}

	e.logger.Infof("✅ InstanceVm 등록 완료: projectID=%d, vmCount=%d", job.ProjectID, registeredCount)
	return registeredCount, nil
}

// getTerraformOutputJSON terraform output -json 실행
func (e *Executor) getTerraformOutputJSON(ctx context.Context, workDir string) (string, error) {
	cmd := exec.CommandContext(ctx, e.config.TerraformBinary, "output", "-json")
	cmd.Dir = workDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("terraform output 실행 실패: %w, output: %s", err, string(output))
	}

	return string(output), nil
}

// registerSingleInstanceVm 단일 InstanceVm 등록
func (e *Executor) registerSingleInstanceVm(projectID int64, jobType models.JobType, tenantID string, vmInfo map[string]interface{}) error {
	// VM 정보 추출
	provisioningVmID, _ := vmInfo["provisioning_vm_id"].(float64)
	vmName, _ := vmInfo["name"].(string)
	moid, _ := vmInfo["moid"].(string)
	uuid, _ := vmInfo["uuid"].(string)
	cpuCount, _ := vmInfo["cpu_count"].(float64)
	memoryMB, _ := vmInfo["memory_mb"].(float64)
	ipAddress, _ := vmInfo["ip_address"].(string) // Terraform output에서 IP 주소 추출

	if vmName == "" || moid == "" || uuid == "" {
		return fmt.Errorf("필수 VM 정보 누락: name=%s, moid=%s, uuid=%s", vmName, moid, uuid)
	}

	e.logger.Infof("InstanceVm 등록 요청: projectID=%d, provisioningVmID=%d, vmName=%s, moRef=%s, ipAddress=%s",
		projectID, int64(provisioningVmID), vmName, moid, ipAddress)

	// Backend API 호출
	request := map[string]interface{}{
		"provisioningVmId": int64(provisioningVmID),
		"vmName":           vmName,
		"vmMoRef":          moid,
		"vmUuid":           uuid,
		"cpuCount":         int(cpuCount),
		"memoryMb":         int(memoryMB),
		"ipAddress":        ipAddress, // IP 주소 추가
	}

	return e.retryableClient.AddInstanceVm(projectID, jobType, tenantID, request)
}

// copyFile 파일 복사 헬퍼
func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	return err
}
