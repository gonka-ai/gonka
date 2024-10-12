

---

### **Step-by-Step Guide for Local Setup, Account Registration, and Sending Inference Requests**

---

### **Step 1: Install the `inferenced` Binary**

Before starting, ensure you have the `inferenced` binary installed on your local machine. If you haven’t installed it yet, follow these steps:
1. **Download the binary** from our official repository or website (link provided by your team).
2. Make the binary executable by running:
   ```bash
   chmod +x inferenced
   ```
3. **Move it** to your path or use it from its current location:
   ```bash
   sudo mv inferenced /usr/local/bin/
   ```

Now, you should be ready to use `inferenced` from your terminal.

---

### **Step 2: Create a New Account Locally**

To participate in the network, you need to create a local account. This will generate a public/private keypair and an account address.

1. Run the following command to create a new account:
   ```bash
   inferenced keys add {{account_name}}
   ```

    - This will generate and display your **private key**, **public key**, and **account address**.
    - **IMPORTANT:** Safely back up your private key (this is the only way to access your account and sign requests).

2. You can verify your keys at any time using:
   ```bash
   inferenced keys list
   ```

3. Copy down your **public key** and **account address** from the output, as you’ll need them for the next step.

---

### **Step 3: Register Your Account and Get Test Tokens**

Now that you have your account address and public key, you'll need to submit these to the API to get your account registered and receive some initial coins for making inference requests.

1. Submit your public key and account address via the provided faucet API. Here’s an example using `curl`:

   ```bash
   curl -X POST https://api.yourchain.com/v1/participants \
   -H "Content-Type: application/json" \
   -d '{
         "pub_key": "{{your_public_key}}",
         "address": "{{your_account_address}}"
       }'
   ```

   Replace `{{your_public_key}}` and `{{your_account_address}}` with the values generated in Step 2.

1. The API will register your account and mint some initial tokens for you, which can be used for submitting inference requests. You’ll get a response confirming the successful registration and coin minting.
2. You can verify you have been added and the amount of coin available by another request

```bash
curl -X GET https://api.yourchain.com/v1/participants/{{your_account_address}}
```


---

### **Step 4: Prepare and Sign an Inference Request**

Once your account is set up and funded, you can prepare an inference request, sign it locally using your private key, and then submit it to the inference API.

1. **Prepare your request payload**. Save your request data to a file, for example `request_payload.json`. Here’s a sample of what the payload might look like:

   ```json
   {
     "model": "your_model_name",
     "data": "input_data_for_inference",
     "parameters": {
       "param1": "value1",
       "param2": "value2"
     }
   }
   ```

2. **Sign the payload** using your private key:
   ```bash
   inferenced signature create --account-address {{your_account_address}} --file request_payload.json
   ```

    - Replace `{{your_account_address}}` with the address generated in Step 2.
    - The `--file` flag should point to the file containing your request payload.
    - This command will generate a **signature** based on the payload, which you will include in the next step.

3. **Copy the output signature** from the command. This will be used when submitting your inference request to the API.

---

### **Step 5: Submit the Inference Request to the API**

Now that you have signed your payload, you’re ready to submit the inference request to the API.

1. Use the following `curl` command to submit your signed inference request:
   ```bash
   curl -X POST https://api.yourchain.com/v1/chat/completions \
   -H "Content-Type: application/json" \
   -H "Authorization: {{your_signature}}" \
   -H "X-Requester-Address: {{your_account_address}}" \
   --data-binary @request_payload.json
   ```

    - Replace `{{your_signature}}` with the signature you generated in Step 4.
    - Replace `{{your_account_address}}` with the account address generated in Step 2.
    - The `request_payload.json` file should contain your inference request data.

2. The API will process the inference request, debit the necessary coins from your account, and return the inference result once complete.

---

### **Additional Commands for Key Management**

Here are some additional commands you can use for managing your keys locally:

- **List your keys**:
   ```bash
   inferenced keys list
   ```

- **Export your account’s public key**:
   ```bash
   inferenced keys show {{account_name}} --pubkey
   ```

- **Import an existing account**:
   ```bash
   inferenced keys add {{account_name}} --recover
   ```

- **Delete an account** (use with caution):
   ```bash
   inferenced keys delete {{account_name}}
   ```
  
- **Export your account’s private key** (use carefully!):
   ```bash
   inferenced keys export {{account_name}}
   ```

---

### **Conclusion**

These steps allow you to create and manage your account keys locally, register your account with the network, receive tokens, and sign inference request payloads. Once signed, you can submit those requests to the network for processing.

This flow works entirely with the `inferenced` binary running locally and requires no direct interaction with the chain from the user’s local machine.

