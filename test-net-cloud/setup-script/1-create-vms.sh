# Run second part of this script when creating a new worker!

# Create a simple VM for a control-plane node
gcloud compute instances create k8s-control-plane \
    --project=decentralized-ai \
    --zone=us-central1-a \
    --machine-type=e2-medium \
    --image-family=ubuntu-2204-lts \
    --image-project=ubuntu-os-cloud \
    --boot-disk-size=64GB \
    --boot-disk-type=pd-standard \
    --tags=k8s-control-plane,ssh-access \
    --scopes=https://www.googleapis.com/auth/cloud-platform

# Create IP
gcloud compute addresses create k8s-control-plane-static-ip \
    --project=decentralized-ai \
    --region=us-central1

gcloud compute instances describe k8s-control-plane --zone=us-central1-a --format='value(networkInterfaces[0].accessConfigs[0].name)'

CONTROL_IP=$(gcloud compute addresses describe k8s-control-plane-static-ip --region=us-central1 --format='value(address)')
gcloud compute instances delete-access-config k8s-control-plane \
    --project=decentralized-ai \
    --zone=us-central1-a \
    --access-config-name="external-nat" # Or whatever name you found above

gcloud compute instances add-access-config k8s-control-plane \
    --project=decentralized-ai \
    --zone=us-central1-a \
    --access-config-name="external-nat" \
    --address=$CONTROL_IP

# Now IP is 34.132.221.241

# Create a simple VM for a worker node
WORKER_INSTANCE_NAME="k8s-worker-4"
GCP_PROJECT="decentralized-ai"
GCP_REGION="us-central1"
GCP_ZONE="us-central1-a"

gcloud compute instances create "$WORKER_INSTANCE_NAME" \
    --project="$GCP_PROJECT" \
    --zone="$GCP_ZONE" \
    --machine-type=g2-standard-4 \
    --accelerator=type=nvidia-l4,count=1 \
    --image-family=ubuntu-2204-lts \
    --image-project=ubuntu-os-cloud \
    --boot-disk-size=512GB \
    --boot-disk-type=pd-ssd \
    --maintenance-policy=TERMINATE \
    --restart-on-failure \
    --tags=k8s-worker,gpu-node,ssh-access \
    --scopes=https://www.googleapis.com/auth/cloud-platform

WORKER_STATIC_IP_NAME="$WORKER_INSTANCE_NAME-static-ip"

gcloud compute addresses create "$WORKER_STATIC_IP_NAME" \
    --project="$GCP_PROJECT" \
    --region="$GCP_REGION"

RESERVED_WORKER_IP=$(gcloud compute addresses describe "${WORKER_STATIC_IP_NAME}" \
    --project="${GCP_PROJECT}" \
    --region="${GCP_REGION}" \
    --format='value(address)')

echo "Reserved IP ${RESERVED_WORKER_IP} for ${WORKER_STATIC_IP_NAME}."

ACCESS_CONFIG_NAME=$(gcloud compute instances describe "${WORKER_INSTANCE_NAME}" \
    --project="${GCP_PROJECT}" \
    --zone="${GCP_ZONE}" \
    --format='value(networkInterfaces[0].accessConfigs[0].name)')
echo "Found existing access config name: ${ACCESS_CONFIG_NAME} for instance ${WORKER_INSTANCE_NAME}."

echo "Deleting current access config ${ACCESS_CONFIG_NAME} from ${WORKER_INSTANCE_NAME}..."
gcloud compute instances delete-access-config "${WORKER_INSTANCE_NAME}" \
    --project="${GCP_PROJECT}" \
    --zone="${GCP_ZONE}" \
    --access-config-name="${ACCESS_CONFIG_NAME}" # Use the name found above

# 5. Assign the reserved static external IP to the worker instance
echo "Assigning static IP ${RESERVED_WORKER_IP} to ${WORKER_INSTANCE_NAME}..."
gcloud compute instances add-access-config "${WORKER_INSTANCE_NAME}" \
    --project="${GCP_PROJECT}" \
    --zone="${GCP_ZONE}" \
    --access-config-name="external-nat" \
    --address="${RESERVED_WORKER_IP}"

echo "Static IP ${RESERVED_WORKER_IP} should now be assigned to ${WORKER_INSTANCE_NAME}."
echo "Verify by running: gcloud compute instances describe ${WORKER_INSTANCE_NAME} --zone ${GCP_ZONE} --project ${GCP_PROJECT} | grep natIP"
