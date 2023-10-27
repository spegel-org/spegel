terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "3.78.0"
    }
    local = {
      source  = "hashicorp/local"
      version = "2.4.0"
    }
  }
}

provider "azurerm" {
  features {}
}

resource "azurerm_resource_group" "this" {
  name     = "spegel-benchmark"
  location = "West Europe"
}

resource "azurerm_kubernetes_cluster" "this" {
  name                = "spegel-benchmark"
  location            = azurerm_resource_group.this.location
  resource_group_name = azurerm_resource_group.this.name
  kubernetes_version  = var.kubernetes_version
  dns_prefix          = "spegelbenchmark"
  sku_tier            = "Free"

  default_node_pool {
    name                         = "default"
    zones                        = ["1", "2", "3"]
    orchestrator_version         = var.kubernetes_version
    vm_size                      = var.default_node_pool_vm_size
    node_count                   = 1
    os_disk_size_gb              = local.vm_skus_disk_size_gb[var.default_node_pool_vm_size]
    os_disk_type                 = "Ephemeral"
    enable_auto_scaling          = false
    only_critical_addons_enabled = true
  }

  identity {
    type = "SystemAssigned"
  }
}

resource "azurerm_kubernetes_cluster_node_pool" "this" {
  name                  = "worker"
  kubernetes_cluster_id = azurerm_kubernetes_cluster.this.id
  zones                 = ["1", "2", "3"]
  node_count            = var.node_count
  vm_size               = var.vm_size
  os_disk_type          = "Ephemeral"
  os_disk_size_gb       = local.vm_skus_disk_size_gb[var.vm_size]
  orchestrator_version  = var.kubernetes_version
}

resource "local_file" "kube_config" {
  content  = azurerm_kubernetes_cluster.this.kube_config_raw
  filename = "benchmark.kubeconfig"
}
