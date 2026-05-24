# =============================================================================
# vSphere 연결 설정
# =============================================================================

variable "vsphere_server" {
  description = "vCenter Server 또는 ESXi 호스트의 IP 주소 또는 FQDN"
  type        = string
}

variable "vsphere_user" {
  description = "vSphere 인증을 위한 사용자명 (예: administrator@vsphere.local)"
  type        = string
}

variable "vsphere_password" {
  description = "vSphere 사용자 계정의 비밀번호"
  type        = string
  sensitive   = true
}

# =============================================================================
# vSphere 인프라 설정
# =============================================================================

variable "datacenter" {
  description = "VM을 생성할 vSphere 데이터센터 이름"
  type        = string
}

variable "datastore" {
  description = "VM 디스크를 저장할 데이터스토어 이름"
  type        = string
}

# =============================================================================
# 컴퓨팅 리소스 설정 (둘 중 하나만 설정)
# =============================================================================

variable "compute_cluster" {
  description = "VM을 생성할 vSphere 클러스터 이름 (클러스터 환경 사용 시)"
  type        = string
  default     = null
}

variable "esxi_host" {
  description = "VM을 생성할 ESXi 호스트 IP 또는 FQDN (단일 호스트 환경 사용 시)"
  type        = string
  default     = null
}

# =============================================================================
# Content Library 설정
# =============================================================================

variable "content_library_name" {
  description = "OVF 템플릿이 저장된 Content Library 이름"
  type        = string
}

# =============================================================================
# 네트워크 설정
# =============================================================================

variable "distributed_switch_name" {
  description = "분산 스위치 이름 (클러스터 환경에서 분산 포트그룹 생성 시 사용)"
  type        = string
  default     = null
}

variable "network_folder" {
  description = "네트워크 포트그룹을 생성할 폴더 경로 (클러스터 환경 전용, 예: iTraining-Net)"
  type        = string
  default     = "iTraining-Net"
}

variable "default_vswitch" {
  description = "기본 vSwitch 이름 (단일 호스트 환경에서 포트그룹 생성 시 사용)"
  type        = string
  default     = "vSwitch0"
}

variable "existing_networks" {
  description = "이미 존재하는 네트워크 목록 (자동 생성에서 제외)"
  type        = list(string)
  default     = ["VM Network"]
}

variable "networks" {
  description = <<-EOT
    생성할 네트워크 설정 정보
    키: 네트워크 이름 (고유값)
    값: {
      vlan_id = VLAN ID (100-4094, DB에서 자동 할당된 값)
    }
  EOT
  type = map(object({
    vlan_id = number
  }))
  default = {}
}

# =============================================================================
# 폴더 관리 설정
# =============================================================================

variable "create_folders" {
  description = "VM 폴더를 자동으로 생성할지 여부 (중첩 폴더 지원)"
  type        = bool
  default     = true
}

variable "existing_folders" {
  description = "이미 존재하는 폴더 목록 (자동 생성에서 제외)"
  type        = list(string)
  default     = ["iTraining", "iTraining/test", "iTraining/B2B"]
}

# =============================================================================
# 가상 머신 설정
# =============================================================================

variable "vms" {
  description = <<-EOT
    생성할 VM들의 설정 정보
    키: VM 식별자 (고유값)
    값: {
      name                   = "VM 이름"
      folder                 = "VM이 저장될 폴더 경로 (예: Production/WebServers)"
      content_library_item   = "Content Library에 있는 템플릿 아이템 이름"
      cpu_count              = CPU 코어 수
      memory_mb              = 메모리 크기 (MB)
      disk_size_gb           = 디스크 크기 (GB) - Linked Clone 시 미사용 (optional)
      network_adapters       = [
        {
          network_name = "연결할 네트워크 이름"
          adapter_type = "네트워크 어댑터 타입 (vmxnet3, e1000e 등)"
          create_new   = 새 네트워크 생성 여부 (true/false)
        }
      ]
    }
  EOT
  type = map(object({
    name                 = string
    folder               = string
    content_library_item = string
    cpu_count            = number
    memory_mb            = number
    disk_size_gb         = optional(number, 0)          # Linked Clone 시 미사용 (템플릿 크기 상속)
    scsi_type            = optional(string, "pvscsi")  # SCSI 컨트롤러 타입 (pvscsi, lsilogic-sas)
    firmware             = optional(string, "bios")    # 부트 펌웨어 (Windows: efi, Linux: bios)
    network_adapters = list(object({
      network_name = string
      adapter_type = string
      create_new   = bool
    }))
  }))
}
