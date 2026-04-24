variable "ssh_private_key_path" {
  description = "Path to the SSH private key Terraform should use for all three hosts."
  type        = string
}

variable "ssh_user" {
  description = "SSH user present on all hosts."
  type        = string
  default     = "decentai"
}

variable "deploy_dir" {
  description = "Remote directory where Terraform uploads scripts and from which launch.py runs."
  type        = string
  default     = "/srv/dai"
}

variable "repo_branch" {
  description = "Git ref passed to launch.py. Keep this on the stable 0.2.10-ready branch."
  type        = string
  default     = "origin/testnet/main"
}

variable "chain_id" {
  description = "Chain ID for the testnet."
  type        = string
  default     = "gonka-testnet-3"
}

variable "public_domain" {
  description = "Single public domain used by the three Filfox/Nebius nodes."
  type        = string
  default     = "xj7-5.s.filfox.io"
}

variable "genesis_internal_ip" {
  description = "Genesis private IP used by join nodes for seed RPC."
  type        = string
  default     = "172.18.114.113"
}

variable "join_1_internal_ip" {
  description = "Join-1 private IP. Mostly informational/output for now."
  type        = string
  default     = "172.18.114.125"
}

variable "join_2_internal_ip" {
  description = "Join-2 private IP. Mostly informational/output for now."
  type        = string
  default     = "172.18.114.105"
}

variable "genesis_ssh_port" {
  type    = number
  default = 18220
}

variable "join_1_ssh_port" {
  type    = number
  default = 18225
}

variable "join_2_ssh_port" {
  type    = number
  default = 18226
}

variable "genesis_p2p_port" {
  type    = number
  default = 19239
}

variable "genesis_api_port" {
  type    = number
  default = 19240
}

variable "join_1_p2p_port" {
  type    = number
  default = 19249
}

variable "join_1_api_port" {
  type    = number
  default = 19250
}

variable "join_2_p2p_port" {
  type    = number
  default = 19251
}

variable "join_2_api_port" {
  type    = number
  default = 19252
}

variable "genesis_internal_api_port" {
  description = "Internal host API port that the platform forwards genesis public API to."
  type        = number
  default     = 8000
}

variable "join_1_internal_api_port" {
  description = "Internal host API port that the platform forwards join-1 public API to."
  type        = number
  default     = 8000
}

variable "join_2_internal_api_port" {
  description = "Internal host API port that the platform forwards join-2 public API to."
  type        = number
  default     = 8000
}

variable "hf_home" {
  description = "Shared HuggingFace cache directory on each host."
  type        = string
  default     = "/srv/dai/cache/"
}

variable "keyring_password" {
  description = "Keyring password used by launch.py for validator account creation."
  type        = string
  default     = "12345678"
  sensitive   = true
}

variable "genesis_key_name" {
  description = "Key name used for genesis account creation/funding and proposal signing."
  type        = string
  default     = "gonka-account-key"
}

variable "proxy_port" {
  description = "Proxy container internal config port. Keep default unless your compose setup changes."
  type        = number
  default     = 8080
}

variable "inference_port" {
  description = "Inference container port used by the current deployment scripts."
  type        = number
  default     = 5050
}

variable "snapshot_interval" {
  description = "Snapshot interval passed through to launch.py."
  type        = number
  default     = 200
}

variable "ethereum_network" {
  description = "Bridge configuration carried through the existing nebius scripts."
  type        = string
  default     = "sepolia"
}

variable "beacon_state_url" {
  description = "Bridge beacon state URL used by the existing deployment."
  type        = string
  default     = "https://sepolia.checkpoint-sync.ethpandaops.io"
}
