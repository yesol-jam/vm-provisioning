# =============================================================================
# 네트워크 포트그룹 생성
# =============================================================================

# ESXi 호스트용 포트그룹 생성 (단일 호스트 환경)
# 단일 호스트 환경에서는 폴더 배치가 지원되지 않음
resource "vsphere_host_port_group" "new_networks" {
  for_each = var.esxi_host != null ? local.create_networks : {}

  name                = each.key
  host_system_id      = data.vsphere_host.host[0].id
  virtual_switch_name = each.value.vswitch_name
  vlan_id             = each.value.vlan_id

  lifecycle {
    ignore_changes        = []
    create_before_destroy = false  # 삭제 순서 문제 방지
  }
}

# 네트워크 생성 후 안정화 대기 (vSphere 전파 시간 확보)
resource "null_resource" "network_stabilization" {
  count = var.esxi_host != null && length(local.create_networks) > 0 ? 1 : 0

  # 네트워크 생성 완료 후에만 실행
  depends_on = [vsphere_host_port_group.new_networks]

  # 네트워크 변경 시 재실행
  triggers = {
    network_names = join(",", keys(local.create_networks))
  }

  # 15초 대기 (vSphere가 네트워크를 전파할 시간 제공)
  provisioner "local-exec" {
    command = "echo 'Waiting for network propagation...' && sleep 15"
  }
}

# =============================================================================
# 네트워크 폴더 관리 (클러스터 환경 전용)
# =============================================================================

# 네트워크 폴더 생성 (iTraining-Net)
# 클러스터 환경에서 분산 포트그룹을 정리하기 위한 폴더
resource "vsphere_folder" "network_folder" {
  count         = var.compute_cluster != null && var.network_folder != null && length(local.create_networks) > 0 ? 1 : 0
  path          = var.network_folder
  type          = "network"
  datacenter_id = data.vsphere_datacenter.dc.id

  lifecycle {
    # 폴더가 이미 존재하면 import 필요
    # terraform import vsphere_folder.network_folder /Datacenter/network/iTraining-Net
    ignore_changes = [path]
  }
}

# 클러스터용 분산 포트그룹 생성 (클러스터 환경)
# iTraining-Net 폴더 아래에 포트그룹 생성
resource "vsphere_distributed_port_group" "cluster_networks" {
  for_each = var.compute_cluster != null ? local.create_networks : {}

  name                            = each.key
  distributed_virtual_switch_uuid = data.vsphere_distributed_virtual_switch.dvs[0].id
  vlan_id                         = each.value.vlan_id

  number_of_ports = 8
  auto_expand     = true

  depends_on = [vsphere_folder.network_folder]

  lifecycle {
    create_before_destroy = false  # 삭제 순서 문제 방지
  }
}

# 네트워크 생성 후 안정화 대기 (클러스터 환경)
resource "null_resource" "cluster_network_stabilization" {
  count = var.compute_cluster != null && length(local.create_networks) > 0 ? 1 : 0

  # 네트워크 생성 완료 후에만 실행
  depends_on = [vsphere_distributed_port_group.cluster_networks]

  # 네트워크 변경 시 재실행
  triggers = {
    network_names = join(",", keys(local.create_networks))
  }

  # 15초 대기 (vSphere가 네트워크를 전파할 시간 제공)
  provisioner "local-exec" {
    command = "echo 'Waiting for network propagation...' && sleep 15"
  }
}

# 분산 스위치 데이터 소스 (클러스터 환경용)
data "vsphere_distributed_virtual_switch" "dvs" {
  count         = var.compute_cluster != null && var.distributed_switch_name != null ? 1 : 0
  name          = var.distributed_switch_name
  datacenter_id = data.vsphere_datacenter.dc.id
}
