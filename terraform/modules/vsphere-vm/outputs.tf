# =============================================================================
# 가상 머신 정보 출력
# =============================================================================

output "vm_info" {
  description = "생성된 VM들의 기본 정보"
  value = {
    for vm_key, vm in vsphere_virtual_machine.vm : vm_key => {
      provisioning_vm_id = tonumber(replace(vm_key, "vm-", ""))
      name               = vm.name
      moid               = vm.moid
      uuid               = vm.uuid
      ip_address         = vm.default_ip_address
      folder             = vm.folder
      cpu_count          = vm.num_cpus
      memory_mb          = vm.memory
    }
  }
}

output "vm_names" {
  description = "생성된 VM 이름 목록"
  value       = [for vm in vsphere_virtual_machine.vm : vm.name]
}

output "vm_ip_addresses" {
  description = "생성된 VM들의 IP 주소"
  value = {
    for vm_key, vm in vsphere_virtual_machine.vm : vm.name => vm.default_ip_address
  }
}

output "vm_uuids" {
  description = "생성된 VM들의 UUID"
  value = {
    for vm_key, vm in vsphere_virtual_machine.vm : vm.name => vm.uuid
  }
}

# =============================================================================
# 네트워크 정보 출력
# =============================================================================

output "vm_network_summary" {
  description = "각 VM별 네트워크 연결 요약"
  value = {
    for vm_key, vm in vsphere_virtual_machine.vm : vm.name => [
      for idx, nic in vm.network_interface : {
        nic_index = idx + 1
        network_name = [
          for name, id in local.network_ids : name
          if id == nic.network_id
        ][0]
        mac_address = nic.mac_address
      }
    ]
  }
}

output "created_networks" {
  description = "새로 생성된 네트워크 목록"
  value = merge(
    {
      for name, pg in vsphere_host_port_group.new_networks :
      name => {
        id           = pg.id
        vswitch_name = pg.virtual_switch_name
        vlan_id      = pg.vlan_id
      }
    },
    {
      for name, pg in vsphere_distributed_port_group.cluster_networks :
      name => {
        id      = pg.id
        vlan_id = pg.vlan_id
      }
    }
  )
}

# =============================================================================
# 폴더 정보 출력
# =============================================================================

output "created_folders" {
  description = "생성된 VM 폴더 경로"
  value = concat(
    [for folder in vsphere_folder.vm_folders_level2 : folder.path],
    [for folder in vsphere_folder.vm_folders_level3 : folder.path],
    [for folder in vsphere_folder.vm_folders_level4 : folder.path]
  )
}

# =============================================================================
# 요약 정보
# =============================================================================

output "deployment_summary" {
  description = "배포 요약 정보"
  value = {
    total_vms             = length(vsphere_virtual_machine.vm)
    total_networks_created = length(vsphere_host_port_group.new_networks) + length(vsphere_distributed_port_group.cluster_networks)
    total_folders_created = length(vsphere_folder.vm_folders_level2) + length(vsphere_folder.vm_folders_level3) + length(vsphere_folder.vm_folders_level4)
    datacenter            = var.datacenter
    datastore             = var.datastore
    content_library       = var.content_library_name
  }
}
