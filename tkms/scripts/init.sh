#!/bin/sh

echo "📂 Directory /root/.tmkms contents after init:"
ls -la /root/.tmkms

if [ "$WITH_KEYGEN" = "true" ]; then
  echo "🔐 WITH_KEYGEN is true — generating new consensus key..."
  tmkms softsign keygen /root/.tmkms/secrets/priv_validator_key.json
else
  echo "📥 WITH_KEYGEN is not set to true — using provided key and state"
  cp priv_validator_key.json /root/.tmkms/secrets/priv_validator_key.json
  cp priv_validator_state.json /root/.tmkms/state/priv_validator_state.json
fi

echo "🔐 Importing key..."
tmkms softsign import /root/.tmkms/secrets/priv_validator_key.json /root/.tmkms/secrets/priv_validator_key.softsign

echo "📝 Contents of tmkms.toml:"
cp tmkms.toml /root/.tmkms/tmkms.toml
cat /root/.tmkms/tmkms.toml

tmkms start -c /root/.tmkms/tmkms.toml
