# 设置您的链节点
## 前提条件
### 依赖项
您需要安装 Docker 来处理容器的启动。
### 文件
您需要两个文件来开始：
1. launch_chain.sh - 这是将使用参数启动您的链的脚本
2. docker-compose-local.yml - 这是一个用于在本地启动链的 docker-compose 文件，由 launch_chain.sh 调用
### config.env
首先，您需要创建一个包含以下环境变量的文件：
* KEY_NAME - 密钥的名称，以及将要创建的两个节点（API 和节点）的名称。
* NODE_CONFIG - 这是一个包含推理节点信息的文件路径（见下文）
* ADD_ENDPOINT - 这是链中已存在端点的公共 URL，将用于将您的账户添加到链中。
* PORT - 这是将为您的 API 端点暴露的本地端口
* PUBLIC_IP - 这是您将用于暴露自己端点的主机地址。它最终需要映射到在此过程中创建的 API 容器。
## NODE_CONFIG
这是一个 JSON 文件，定义了实际为您的节点提供推理请求的推理服务器端点。格式如下：
```
[
    {
        "id": "uniqueNodeName",
        "url": "http://35.76.234.56:8080/",
        "max_concurrent": 10,
        "models": [
            "Qwen/Qwen2.5-7B-Instruct"
        ]
    }
]
```
`max_concurrent` 指定给定端点一次可以处理多少个请求。API 将在您指定的所有端点之间平衡所有请求。
## 启动
创建好包含正确设置的 config.env 文件后，只需运行 `./launch_chain.sh` —— 这将：
1. 启动您的节点，该节点将使用 Docker 镜像中指定的种子节点连接到链，然后将您添加为网络的参与者。赶上当前链高度需要一些时间。
2. 启动您的 API，它将连接到您的节点，并成为您的主要入口点，提供推理请求并允许您管理节点。
## 自定义部署
您的环境可能有所不同，取决于您想在哪里部署以及如何部署。但您可以使用 `launch_chain.sh` 和 `docker-compose-local.yml` 作为自己部署的起点。
重要要点：
1. 使用 `docker-compose-local.yml` 中包含的 Docker 镜像——这些是将用于加入链的镜像。它们包含所有必要的创世状态和种子节点。
2. 每个 Docker 容器都需要设置一个 "KEY_NODE" 作为密钥和 API 的主 URL。
3. 添加推理节点通过 /v1/nodes/batch 端点完成。您可以在 `launch_chain.sh` 中看到调用。
4. 要将您的节点添加到网络中，您仍然需要一个已加入节点的 URL。`launch_chain.sh` 展示了如何获取负载数据（通过在节点上执行命令）以及如何构建它。
