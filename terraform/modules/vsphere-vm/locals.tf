# =============================================================================
# 로컬 변수 정의
# =============================================================================

locals {
  # 기본 네트워크 이름 (existing_networks가 null이거나 비어있으면 "VM Network" 사용)
  default_network = try(var.existing_networks[0], "VM Network")

  # VM에서 사용하는 모든 네트워크 이름 수집 (중복 제거)
  # Single VM Isolation: network_adapters가 비어있으면 기본 네트워크 포함
  all_network_names = toset(flatten([
    for vm_key, vm in var.vms :
      length(vm.network_adapters) > 0 ? [
        for adapter in vm.network_adapters : adapter.network_name
      ] : [local.default_network]  # 기본 네트워크 (예: "VM Network")
  ]))

  # 기존 네트워크 목록 (null 안전성 처리)
  existing_networks = var.existing_networks != null ? toset(var.existing_networks) : toset([])

  # 새로 생성해야 할 네트워크 (create_new = true인 것들)
  networks_to_create = toset(flatten([
    for vm_key, vm in var.vms : [
      for adapter in vm.network_adapters : adapter.network_name
      if adapter.create_new
    ]
  ]))

  # 기존 네트워크에서 조회할 것들 (create_new = false이거나 existing_networks에 포함)
  existing_network_names = setsubtract(local.all_network_names, local.networks_to_create)

  # 새로 생성할 네트워크 설정
  create_networks = {
    for name in local.networks_to_create : name => {
      vswitch_name = var.default_vswitch
      vlan_id      = lookup(var.networks, name, null) != null ? var.networks[name].vlan_id : 0
    }
  }

  # 리소스 풀 ID 결정
  # 클러스터 또는 호스트 중 설정된 것의 리소스 풀을 사용
  resource_pool_id = var.compute_cluster != null ? data.vsphere_compute_cluster.cluster[0].resource_pool_id : data.vsphere_host.host[0].resource_pool_id

  # 네트워크 ID 맵 생성
  network_ids = merge(
    # 기존 네트워크 ID들
    {
      for name, network in data.vsphere_network.existing_networks :
      name => network.id
    },
    # 새로 생성된 네트워크 ID들
    var.esxi_host != null ? {
      for name, network in vsphere_host_port_group.new_networks :
      name => data.vsphere_network.created_networks[name].id
    } : {
      for name, network in vsphere_distributed_port_group.cluster_networks :
      name => data.vsphere_network.created_networks[name].id
    }
  )

  # 기존 폴더 목록 (null 안전성 처리)
  existing_folders = var.existing_folders != null ? toset(var.existing_folders) : toset([])

  # VM 폴더 경로 처리 (기존 폴더 제외)
  all_folder_paths = setsubtract(
    toset(flatten([
      for vm_key, vm in var.vms : [
        for i in range(1, length(split("/", vm.folder)) + 1) :
          join("/", slice(split("/", vm.folder), 0, i))
      ] if vm.folder != null && vm.folder != ""
    ])),
    local.existing_folders
  )
}
