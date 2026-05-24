# =============================================================================
# Terraform 설정 및 Provider 구성
# =============================================================================

terraform {
  required_providers {
    vsphere = {
      source  = "hashicorp/vsphere"
      version = "~> 2.0"
    }
    null = {
      source  = "hashicorp/null"
      version = "~> 3.0"
    }
  }
  required_version = ">= 1.0"

  # S3-compatible backend for state storage (S3/MinIO)
  # STATE_BACKEND_ENABLED=true 환경변수 설정 시 활성화됨
  # 개발/테스트 환경에서는 local backend 사용 (주석 처리)
  # backend "s3" {
  #   # bucket, key, region 등은 런타임에 -backend-config로 전달됨
  #   # 예: terraform init -backend-config="bucket=terraform-state" \
  #   #                    -backend-config="key=project-123/terraform.tfstate" \
  #   #                    -backend-config="region=us-east-1"
  #
  #   # MinIO 사용 시 추가 설정:
  #   # -backend-config="endpoint=https://minio.example.com"
  #   # -backend-config="skip_credentials_validation=true"
  #   # -backend-config="skip_metadata_api_check=true"
  #   # -backend-config="force_path_style=true"
  #
  #   # State locking (DynamoDB 또는 대안)
  #   # dynamodb_table = "terraform-state-lock"
  #
  #   # State 암호화 (S3 서버 사이드 암호화)
  #   encrypt = true
  # }
}

# vSphere Provider 설정
provider "vsphere" {
  user                 = var.vsphere_user
  password             = var.vsphere_password
  vsphere_server       = var.vsphere_server
  allow_unverified_ssl = true
}

# =============================================================================
# 데이터 소스 정의 - 기존 vSphere 리소스 조회
# =============================================================================

# 데이터센터 정보 조회
data "vsphere_datacenter" "dc" {
  name = var.datacenter
}

# 클러스터 데이터 소스 (클러스터 환경 사용 시)
data "vsphere_compute_cluster" "cluster" {
  count         = var.compute_cluster != null ? 1 : 0
  name          = var.compute_cluster
  datacenter_id = data.vsphere_datacenter.dc.id
}

# ESXi 호스트 데이터 소스 (단일 호스트 환경 사용 시)
data "vsphere_host" "host" {
  count         = var.esxi_host != null ? 1 : 0
  name          = var.esxi_host
  datacenter_id = data.vsphere_datacenter.dc.id
}

# 데이터스토어 정보 조회
data "vsphere_datastore" "datastore" {
  name          = var.datastore
  datacenter_id = data.vsphere_datacenter.dc.id
}

# Content Library 조회
data "vsphere_content_library" "library" {
  name = var.content_library_name
}

# 각 VM에 대한 Content Library Item 조회
data "vsphere_content_library_item" "template" {
  for_each   = var.vms
  name       = each.value.content_library_item
  library_id = data.vsphere_content_library.library.id
  type       = "ovf"
}

# NOTE: Content Library OVF 배포 시 guest_id, scsi_type, disk 설정은 템플릿에서 자동 상속됨
# 기존 vsphere_virtual_machine 데이터 소스는 같은 이름의 VM이 여러 개 있을 경우 충돌 발생하므로 제거

# =============================================================================
# VM 폴더 생성 (깊이별 순차 생성)
# =============================================================================

# 레벨 2 폴더 (예: iTraining/project-1)
resource "vsphere_folder" "vm_folders_level2" {
  for_each = var.create_folders ? toset([
    for path in local.all_folder_paths : path
    if length(split("/", path)) == 2
  ]) : toset([])

  path          = each.value
  type          = "vm"
  datacenter_id = data.vsphere_datacenter.dc.id

  lifecycle {
    ignore_changes = []
  }
}

# 레벨 3 폴더 (예: iTraining/project-1/vmName) - 레벨 2 완료 후 생성
resource "vsphere_folder" "vm_folders_level3" {
  for_each = var.create_folders ? toset([
    for path in local.all_folder_paths : path
    if length(split("/", path)) == 3
  ]) : toset([])

  path          = each.value
  type          = "vm"
  datacenter_id = data.vsphere_datacenter.dc.id

  depends_on = [vsphere_folder.vm_folders_level2]

  lifecycle {
    ignore_changes = []
  }
}

# 레벨 4 폴더 (필요시) - 레벨 3 완료 후 생성
resource "vsphere_folder" "vm_folders_level4" {
  for_each = var.create_folders ? toset([
    for path in local.all_folder_paths : path
    if length(split("/", path)) >= 4
  ]) : toset([])

  path          = each.value
  type          = "vm"
  datacenter_id = data.vsphere_datacenter.dc.id

  depends_on = [vsphere_folder.vm_folders_level3]

  lifecycle {
    ignore_changes = []
  }
}

# =============================================================================
# 네트워크 데이터 소스 - 기존 및 새로 생성된 네트워크 조회
# =============================================================================

# 기존 네트워크 정보 조회
data "vsphere_network" "existing_networks" {
  for_each      = local.existing_network_names
  name          = each.value
  datacenter_id = data.vsphere_datacenter.dc.id
}

# 새로 생성된 네트워크의 실제 vSphere ID 조회
data "vsphere_network" "created_networks" {
  for_each      = local.networks_to_create
  name          = each.value
  datacenter_id = data.vsphere_datacenter.dc.id

  depends_on = [
    vsphere_host_port_group.new_networks,
    vsphere_distributed_port_group.cluster_networks,
    null_resource.network_stabilization,
    null_resource.cluster_network_stabilization
  ]
}

# =============================================================================
# 가상 머신 생성 (Content Library OVF 배포)
# =============================================================================

resource "vsphere_virtual_machine" "vm" {
  for_each = var.vms

  # 기본 VM 설정
  name             = each.value.name
  resource_pool_id = local.resource_pool_id
  datastore_id     = data.vsphere_datastore.datastore.id
  folder           = each.value.folder

  # 하드웨어 사양 설정
  num_cpus = each.value.cpu_count
  memory   = each.value.memory_mb

  # SCSI 컨트롤러 타입 (Windows: lsilogic-sas, Linux: pvscsi)
  scsi_type = each.value.scsi_type

  # 부트 펌웨어 (Windows: efi, Linux: bios)
  firmware = each.value.firmware

  # IP 주소 대기 비활성화
  wait_for_guest_net_timeout = 0
  wait_for_guest_ip_timeout  = 0

  # 네트워크 어댑터 동적 생성
  # Single VM Isolation: network_adapters가 비어있으면 기본 네트워크 사용
  dynamic "network_interface" {
    for_each = length(each.value.network_adapters) > 0 ? each.value.network_adapters : [
      {
        network_name = local.default_network  # 기본값: "VM Network"
        adapter_type = "vmxnet3"
        create_new   = false
      }
    ]
    content {
      network_id   = local.network_ids[network_interface.value.network_name]
      adapter_type = network_interface.value.adapter_type
    }
  }

  # Linked Clone: 디스크 설정은 템플릿에서 상속되지만 disk 블록은 필수
  # size는 템플릿과 동일하게 유지됨 (linked clone은 델타 디스크 사용)
  disk {
    label            = "disk0"
    size             = each.value.disk_size_gb > 0 ? each.value.disk_size_gb : 40  # 기본값 40GB
    eagerly_scrub    = false
    thin_provisioned = true
  }

  # Content Library OVF에서 Linked Clone (빠른 배포, 델타 디스크 사용)
  clone {
    template_uuid = data.vsphere_content_library_item.template[each.key].id
    linked_clone  = true
  }

  # 리소스 생성 순서 보장
  depends_on = [
    vsphere_folder.vm_folders_level2,
    vsphere_folder.vm_folders_level3,
    vsphere_folder.vm_folders_level4,
    vsphere_host_port_group.new_networks,
    vsphere_distributed_port_group.cluster_networks,
    null_resource.network_stabilization,
    null_resource.cluster_network_stabilization,
    data.vsphere_network.existing_networks,
    data.vsphere_network.created_networks
  ]
}

# =============================================================================
# VM 생성 후 전원 끄기
# hashicorp/vsphere provider에서 power_on 속성이 지원되지 않으므로
# vSphere REST API를 사용하여 VM 생성 후 전원을 끔
# =============================================================================

resource "null_resource" "vm_power_off" {
  for_each = var.vms

  # VM 생성이 완료된 후에만 실행
  depends_on = [vsphere_virtual_machine.vm]

  # VM ID가 변경될 때마다 재실행
  triggers = {
    vm_id = vsphere_virtual_machine.vm[each.key].moid
  }

  provisioner "local-exec" {
    interpreter = ["/bin/bash", "-c"]
    command     = <<-EOT
      # vSphere REST API를 사용하여 VM 전원 끄기
      VM_ID="${vsphere_virtual_machine.vm[each.key].moid}"
      VCENTER_HOST="${var.vsphere_server}"
      VCENTER_USER="${var.vsphere_user}"
      VCENTER_PASS="${var.vsphere_password}"

      echo "VM 전원 끄기 시작: $VM_ID"

      # 세션 획득
      SESSION_ID=$(curl -sk -X POST \
        -u "$VCENTER_USER:$VCENTER_PASS" \
        "https://$VCENTER_HOST/api/session" 2>/dev/null)

      # 따옴표 제거
      SESSION_ID=$(echo $SESSION_ID | tr -d '"')

      if [ -z "$SESSION_ID" ]; then
        echo "vCenter 세션 획득 실패"
        exit 0  # 실패해도 Terraform 실패로 처리하지 않음
      fi

      # VM 전원 상태 확인
      POWER_STATE=$(curl -sk -X GET \
        -H "vmware-api-session-id: $SESSION_ID" \
        "https://$VCENTER_HOST/api/vcenter/vm/$VM_ID/power" 2>/dev/null | grep -o '"state":"[^"]*"' | cut -d'"' -f4)

      echo "현재 전원 상태: $POWER_STATE"

      # VM이 켜져있으면 전원 끄기
      if [ "$POWER_STATE" = "POWERED_ON" ]; then
        echo "VM 전원 끄기 요청..."
        curl -sk -X POST \
          -H "vmware-api-session-id: $SESSION_ID" \
          "https://$VCENTER_HOST/api/vcenter/vm/$VM_ID/power?action=stop" 2>/dev/null

        sleep 2  # 전원 끄기 완료 대기

        # 결과 확인
        NEW_STATE=$(curl -sk -X GET \
          -H "vmware-api-session-id: $SESSION_ID" \
          "https://$VCENTER_HOST/api/vcenter/vm/$VM_ID/power" 2>/dev/null | grep -o '"state":"[^"]*"' | cut -d'"' -f4)
        echo "전원 끄기 완료: $NEW_STATE"
      else
        echo "VM이 이미 꺼져있음 (또는 상태 확인 불가)"
      fi

      # 세션 정리
      curl -sk -X DELETE \
        -H "vmware-api-session-id: $SESSION_ID" \
        "https://$VCENTER_HOST/api/session" 2>/dev/null || true

      echo "VM 전원 끄기 처리 완료: $VM_ID"
    EOT
  }
}
