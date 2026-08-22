# 消费者设置指南
创建开发者账户并向 Gonka 网络发送推理请求的分步指南。
---
## 1. 定义变量
在开始之前，设置所需的环境变量：
```bash
export ACCOUNT_NAME="myaccount"
export NODE_URL="http://node2.gonka.ai:8000"
```
| 变量 | 描述 |
|---|---|
| `ACCOUNT_NAME` | 您账户的本地密钥环名称。仅存在于您的机器上，不会记录在链上。 |
| `NODE_URL` | 任意 Gonka 节点的 URL。用于账户查询、推理请求和链上操作。 |
将 `myaccount` 替换为您喜欢的任何名称，并从下面的列表中选择一个节点 URL。
<details>
<summary><b>创世节点</b></summary>
```
http://node1.gonka.ai:8000
http://node2.gonka.ai:8000
http://node3.gonka.ai:8000
http://185.216.21.98:8000
http://36.189.234.197:18026
http://36.189.234.237:17241
http://47.236.26.199:8000
http://47.236.19.22:18000
http://gonka.spv.re:8000
```
</details>
<details>
<summary><b>如何选择一个随机活跃节点</b></summary>
获取当前活跃参与者列表，并选择任意一个 `inference_url`：
```bash
curl "$NODE_URL/v1/epochs/current/participants"
```
使用随机节点可以改善网络去中心化和负载分配。
</details>
---
## 2. 安装 `inferenced` CLI
从[官方仓库](https://github.com/gonka-ai/gonka)下载适用于您系统的最新 `inferenced` 二进制文件。
```bash
chmod +x inferenced
sudo mv inferenced /usr/local/bin/
inferenced version
```
**macOS 用户：** 如果看到安全警告，请前往 **系统设置 → 隐私与安全性**，然后点击"仍要允许"。
---
## 3. 创建账户
创建一个将用于签名推理请求的本地密钥对。
```bash
inferenced keys add "$ACCOUNT_NAME"
```
输出包含您的**地址**、**公钥**和**助记词短语**。
> **重要提示：** 请安全备份助记词短语和私钥——它们是恢复账户和签名请求的唯一方式。
保存地址以备后续步骤使用：
```bash
export GONKA_ADDRESS="<输出中的地址>"
```
在步骤 5 中，您还需要私钥来配置客户端连接器。如何存储和提供私钥（环境变量、密钥管理器、`.env` 文件等）由您决定。
---
## 4. 为账户充值并发布公钥
要发送推理请求，您的账户必须有正余额，并且其公钥必须发布在链上。
您**不需要**注册为参与者——这仅是推理托管所需要的。
### 4.1 为账户充值
您的账户需要正余额来支付推理费用。有关钱包、余额和转账的完整指南，请参阅[钱包与转账指南](https://gonka.ai/wallet/wallet-and-transfer-guide/)。
您可以随时查看当前余额：
```bash
inferenced query bank balances "$GONKA_ADDRESS" --node "$NODE_URL/chain-rpc"
```
要从另一个钱包为账户充值，发送任意数量的 `ngonka`：
```bash
inferenced tx bank send <发送者密钥名称> "$GONKA_ADDRESS" 1000000ngonka \
  --chain-id gonka-mainnet \
  --node "$NODE_URL/chain-rpc"
```
您也可以使用 [Keplr](https://gonka.ai/wallet/wallet-and-transfer-guide/#send-coins) 或 [Leap](https://gonka.ai/wallet/wallet-and-transfer-guide/#send-coins) 钱包进行转账。
### 4.2 在链上发布公钥
账户充值后，发布您的公钥：
```bash
inferenced publish-pubkey \
  --from "$ACCOUNT_NAME" \
  --node "$NODE_URL/chain-rpc" \
  --yes
```
这会执行一次最小化的自我转账（`1ngonka`），将您的公钥注册到区块链上。
> 如果出现 `rpc error: code = NotFound ... account ... not found` 错误，说明您的账户尚未收到代币——请先完成步骤 4.1。
### 4.3 验证账户
```bash
curl -s "$NODE_URL/v2/accounts/$GONKA_ADDRESS" | jq .
```
响应应包含 `pubkey`、`balance` 和 `denom`。
---
## 5. 发送推理请求
Gonka 使用修改版的 OpenAI SDK，自动使用您的私钥签名每个请求。所有支持语言的完整文档和示例请参见 [gonka-openai 仓库](https://github.com/gonka-ai/gonka-openai/)。
### 最小 Python 示例
```bash
pip install gonka-openai
```
```python
from gonka_openai import GonkaOpenAI
client = GonkaOpenAI(
    gonka_private_key="<您的私钥>",  # 步骤 3 中的十六进制编码私钥
    source_url="<NODE_URL>",          # 步骤 1 中的同一节点 URL
)
response = client.chat.completions.create(
    model="Qwen/Qwen2.5-7B-Instruct",
    messages=[{"role": "user", "content": "你好！"}],
    max_tokens=128,
)
print(response.choices[0].message.content)
```
> 如果出现 `Insufficient balance` 错误，请为账户充值更多代币或降低 `max_tokens`。
---
## 密钥管理参考
```bash
# 列出所有账户
inferenced keys list
# 显示公钥
inferenced keys show "$ACCOUNT_NAME" --pubkey
# 从助记词恢复账户
inferenced keys add "$ACCOUNT_NAME" --recover
# 删除账户（谨慎使用）
inferenced keys delete "$ACCOUNT_NAME"
# 导出私钥（谨慎使用）
inferenced keys export "$ACCOUNT_NAME" --unarmored-hex --unsafe
```
