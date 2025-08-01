alias k3s-tunnel="gcloud compute ssh k8s-control-plane \
    --project=decentralized-ai \
    --zone=us-central1-a \
    -- -L 6443:127.0.0.1:6443 -N -f && echo 'SSH tunnel to k8s-control-plane port 6443 established on local port 6443.'"

# Check status of the k3s tunnel
alias k3s-tunnel-status="if pgrep -f 'ssh.*-L 6443:127.0.0.1:6443' > /dev/null; then \
    echo 'k3s tunnel is ACTIVE'; \
    echo 'PID: '$(pgrep -f 'ssh.*-L 6443:127.0.0.1:6443'); \
  else \
    echo 'k3s tunnel is NOT ACTIVE'; \
  fi"

# Kill the k3s tunnel
alias k3s-tunnel-kill="if pgrep -f 'ssh.*-L 6443:127.0.0.1:6443' > /dev/null; then \
    echo 'Terminating k3s tunnel...'; \
    pkill -f 'ssh.*-L 6443:127.0.0.1:6443' && echo 'k3s tunnel terminated successfully.'; \
  else \
    echo 'No active k3s tunnel found.'; \
  fi"
