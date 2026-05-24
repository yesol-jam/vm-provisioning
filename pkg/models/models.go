package models

// ExecutionType Terraform 실행 타입
type ExecutionType string

const (
	ExecutionTypePlan    ExecutionType = "PLAN"
	ExecutionTypeApply   ExecutionType = "APPLY"
	ExecutionTypeDestroy ExecutionType = "DESTROY"
)

// ExecutionStatus 실행 상태
type ExecutionStatus string

const (
	ExecutionStatusQueued  ExecutionStatus = "QUEUED"
	ExecutionStatusRunning ExecutionStatus = "RUNNING"
	ExecutionStatusSuccess ExecutionStatus = "SUCCESS"
	ExecutionStatusFailed  ExecutionStatus = "FAILED"
)

// JobType 작업 타입 (테넌트 vs 테스트)
type JobType string

const (
	JobTypeTenant JobType = "TENANT" // 테넌트 인스턴스 프로비저닝
	JobTypeTest   JobType = "TEST"   // 플랫폼 테스트 프로비저닝
)

// TerraformJobPayload Redis 큐에서 수신하는 작업 페이로드
type TerraformJobPayload struct {
	JobType       JobType       `json:"jobType"`             // TENANT or TEST
	TenantID      string        `json:"tenantId,omitempty"`  // TENANT 작업 시 필수, TEST 작업 시 빈값
	ProjectID     int64         `json:"projectId"`
	ExecutionID   int64         `json:"executionId"`
	Type          ExecutionType `json:"type"`
	BackendAPIURL string        `json:"backendApiUrl"`
	ExecutedBy    *int64        `json:"executedBy,omitempty"`
}

// IsTenantJob 테넌트 작업인지 확인
func (p *TerraformJobPayload) IsTenantJob() bool {
	return p.JobType == "" || p.JobType == JobTypeTenant
}

// IsTestJob 테스트 작업인지 확인
func (p *TerraformJobPayload) IsTestJob() bool {
	return p.JobType == JobTypeTest
}

// TerraformProjectDetail Backend API에서 받는 프로젝트 상세 정보
type TerraformProjectDetail struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`

	// vCenter 연결 정보
	VcenterServer   string `json:"vcenterServer"`
	VcenterUsername string `json:"vcenterUsername"`
	VcenterPassword string `json:"vcenterPassword"`

	// 인프라 설정
	Datacenter        string `json:"datacenter"`
	Datastore         string `json:"datastore"`
	ComputeCluster    string `json:"computeCluster,omitempty"`
	EsxiHost          string `json:"esxiHost,omitempty"`
	DistributedSwitch string `json:"distributedSwitch,omitempty"`
	DefaultVswitch    string `json:"defaultVswitch,omitempty"`
	ExistingNetworks  string `json:"existingNetworks,omitempty"` // JSON array string

	// Content Library 정보
	ContentLibraryID        *int64 `json:"contentLibraryId,omitempty"`
	ContentLibraryName      string `json:"contentLibraryName,omitempty"`
	ContentLibraryVsphereID string `json:"contentLibraryVsphereId,omitempty"`

	// VM 목록
	VMs []TerraformVMDetail `json:"vms"`

	// 네트워크 목록
	Networks []TerraformNetworkConfig `json:"networks"`
}

// TerraformNetworkConfig 네트워크 설정 (VLAN ID 포함)
type TerraformNetworkConfig struct {
	NetworkName string `json:"networkName"`
	VlanID      int    `json:"vlanId"`
}

// TerraformVMDetail VM 상세 정보
type TerraformVMDetail struct {
	ID                    int64  `json:"id"`
	Name                  string `json:"name"`
	Folder                string `json:"folder,omitempty"`
	TemplateItemID        *int64 `json:"templateItemId,omitempty"`
	TemplateItemName      string `json:"templateItemName,omitempty"`
	TemplateItemVsphereID string `json:"templateItemVsphereId,omitempty"`
	CPUCount              int    `json:"cpuCount"`
	MemoryMB              int    `json:"memoryMb"`
	DiskSizeGB            int    `json:"diskSizeGb"`
	ScsiType              string `json:"scsiType,omitempty"`  // SCSI 컨트롤러 타입 (pvscsi, lsilogic-sas)
	Firmware              string `json:"firmware,omitempty"`  // 부트 펌웨어 (bios, efi)
	DisplayOrder          int    `json:"displayOrder"`

	// 네트워크 어댑터 목록
	Networks []TerraformNetworkDetail `json:"networks"`
}

// TerraformNetworkDetail 네트워크 어댑터 상세 정보
type TerraformNetworkDetail struct {
	ID           int64  `json:"id"`
	NetworkName  string `json:"networkName"`
	AdapterType  string `json:"adapterType"`
	CreateNew    bool   `json:"createNew"`
	DisplayOrder int    `json:"displayOrder"`
}

// APIResponse Backend API 공통 응답 형식
type APIResponse struct {
	Status  string      `json:"status"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

// TerraformVars Terraform tfvars 파일 생성용 구조체
type TerraformVars struct {
	VsphereServer   string `json:"vsphere_server"`
	VsphereUser     string `json:"vsphere_user"`
	VspherePassword string `json:"vsphere_password"`

	Datacenter     string  `json:"datacenter"`
	Datastore      string  `json:"datastore"`
	ComputeCluster *string `json:"compute_cluster,omitempty"`
	EsxiHost       *string `json:"esxi_host,omitempty"`

	ContentLibraryName    string `json:"content_library_name"`
	DistributedSwitchName string `json:"distributed_switch_name,omitempty"`
	DefaultVswitch        string `json:"default_vswitch,omitempty"`
	NetworkFolder         string `json:"network_folder,omitempty"` // 네트워크 폴더 (클러스터 환경 전용, 예: iTraining-Net)

	ExistingNetworks []string `json:"existing_networks"`
	CreateFolders    bool     `json:"create_folders"`

	VMs map[string]VMConfig `json:"vms"`
}

// VMConfig VM 설정
type VMConfig struct {
	Name               string           `json:"name"`
	Folder             string           `json:"folder"`
	ContentLibraryItem string           `json:"content_library_item"`
	CPUCount           int              `json:"cpu_count"`
	MemoryMB           int              `json:"memory_mb"`
	DiskSizeGB         int              `json:"disk_size_gb"`
	ScsiType           string           `json:"scsi_type,omitempty"`  // SCSI 컨트롤러 타입 (pvscsi, lsilogic-sas)
	Firmware           string           `json:"firmware,omitempty"`   // 부트 펌웨어 (bios, efi)
	NetworkAdapters    []NetworkAdapter `json:"network_adapters"`
}

// NetworkAdapter 네트워크 어댑터 설정
type NetworkAdapter struct {
	NetworkName string `json:"network_name"`
	AdapterType string `json:"adapter_type"`
	CreateNew   bool   `json:"create_new"`
}
