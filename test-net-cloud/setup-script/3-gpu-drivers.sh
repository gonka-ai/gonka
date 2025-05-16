# Re-run this script when creating a new worker!
# Check driver version: nvidia-smi
# Check container toolkit version:     nvidia-container-cli --version

# First part, install NVIDIA drivers. only needs to be run on k8s-worker machines
sudo add-apt-repository ppa:graphics-drivers
sudo apt update
sudo apt install -y nvidia-driver-565
sudo reboot

# Check if NVIDIA drivers are installed
nvidia-smi

# Second part, install container toolkit
# From official website: https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html
# Add NVIDIA package repositories
curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg \
  && curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | \
    sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' | \
    sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list

sudo apt-get update
sudo apt-get install -y nvidia-container-toolkit

# Configure containerd (which k3s uses) to use NVIDIA runtime
# K3s ships its own containerd config. We need to be careful.
# We'll let k3s install first, then configure containerd.
# For now, just having the toolkit installed is good.
