# Gonka 计算证明（PoC）设计
本文档描述了 Gonka 网络中完整的计算证明设计和工作流程。PoC 系统基于计算性能而非代币质押来确定验证者共识权重，利用了 [cosmos_changes.md](cosmos_changes.md) 中描述的 Cosmos SDK 修改。
## 系统架构
Gonka 网络由三种主要节点类型组成，它们协同工作以实现 PoC 共识机制：
### 1. ML 节点
- **用途**：执行机器学习计算，用于 PoC 生成和验证
- **位置**：`mlnode/` 包
- **关键组件**：
  - 使用 transformer 模型的证明生成工作器
  - 批量验证能力
  - 用于外部通信的 REST API
### 2. 去中心化 API 节点
- **用途**：编排 PoC 操作并管理 ML 节点协调
- **位置**：`decentralized-api/` 包
- **关键组件**：
  - 用于 ML 节点管理的节点代理
  - 用于工作流协调的 PoC 编排器
  - 用于 epoch 管理的链阶段追踪器
### 3. 推理链节点
- **用途**：运行修改版 Cosmos SDK 的区块链验证者
- **位置**：`inference-chain/` 包
- **关键组件**：
  - PoC 批量和验证消息处理器
  - Epoch 管理和验证者集更新
  - 与 SetComputeValidators 函数的集成
## PoC 工作流程概述
PoC 系统在 epoch 内的不同阶段运行。每个 epoch 代表验证者执行计算工作以确定下一个验证周期的投票权重的时期。
### Epoch 结构
每个 epoch 包含由 `inference-chain/x/inference/types/epoch_context.go` 中的 EpochContext 管理的几个不同阶段：
1. **PoC 生成阶段**：ML 节点生成计算证明批次
2. **PoC 验证阶段**：验证者交叉验证提交的批次
3. **PoC 验证结束阶段**：计算结果并确定新权重
4. **验证者集更新阶段**：具有更新投票权重的新验证者被激活
## 详细 PoC 流程
### 阶段 1：PoC 生成启动
当链到达 PoC 起始区块高度时（由 EpochContext 中的 `IsStartOfPocStage` 确定），发生以下情况：
**链侧（inference-chain/x/inference/module/module.go）**：
- `onStartOfPocStage` 在 EndBlock 处理器中被触发
- 通过 `CreateEpochGroup` 创建新的 epoch
- 基于配置的阈值清理旧的推理和 PoC 数据
**API 节点侧（decentralized-api/internal/event_listener/new_block_dispatcher.go）**：
- OnNewBlockDispatcher 检测 PoC 起始转换
- NodePoCOrchestrator 接收 StartPoCEvent
- 使用区块高度生成随机种子
### 阶段 2：ML 节点 PoC 生成
**API 节点编排（decentralized-api/broker/node_worker_commands.go）**：
- API 节点为每个管理的 ML 节点执行 StartPoCNodeCommand
- 命令通过代理的节点工作器系统分发
- 每个 ML 节点接收初始化参数，包括区块哈希、公钥和回调 URL
**ML 节点计算（mlnode/packages/pow/src/pow/compute/）**：
- ML 节点使用分发的区块哈希作为种子初始化 transformer 模型
- 工作器开始使用 Compute 类生成证明批次
- 每个批次包含从模型输出计算的非ces和距离值
- 生成的批次通过回调机制发送回 API 节点
**批次提交（decentralized-api/internal/server/mlnode/post_generated_batches_handler.go）**：
- API 节点从 ML 节点接收生成的批次
- 批次被转换为 MsgSubmitPocBatch 消息
- 消息通过 cosmos 客户端提交到区块链
### 阶段 3：PoC 验证阶段
当验证阶段开始时（由 `IsStartOfPoCValidationStage` 确定）：
**验证启动（decentralized-api/internal/poc/node_orchestrator.go）**：
- ValidateReceivedBatches 函数被触发
- API 节点从链上查询所有提交的 PoC 批次
- 使用确定性 nonce 选择应用验证采样
- ML 节点通过 InitValidateNodeCommand 切换到验证模式
**ML 节点验证（mlnode/packages/pow/src/pow/compute/compute.py）**：
- ML 节点通过 ValidateBatch API 接收要验证的批次
- validate 方法为给定的 nonces 重新生成证明
- 验证结果包括欺诈检测和统计分析
- 结果作为 MsgSubmitPocValidation 消息发送回去
### 阶段 4：PoC 结果计算
在验证阶段结束时（`IsEndOfPoCValidationStage`）：
**权重计算（inference-chain/x/inference/module/chainvalidation.go）**：
- `ComputeNewWeights` 函数处理所有提交的批次和验证结果
- 从活跃参与者中获取当前验证者权重
- 使用基于多数的逻辑做出 PoC 验证决策
- 根据其他验证者的验证结果接受或拒绝参与者
**验证决策逻辑**：
- 每个参与者的提交由其他网络参与者验证
- 接受需要超过一半按权重计算的参与者的有效验证
- 如果无效验证超过一半按权重计算的参与者，则发生拒绝
- 决策包含欺诈检测阈值和统计分析
### 阶段 5：验证者集更新
在 `IsSetNewValidatorsStage` 期间：
**验证者权重更新（inference-chain/x/inference/module/module.go）**：
- 系统使用计算结果调用 `SetComputeValidators`
- 该函数在修改的 Cosmos SDK 质押模块中实现
- 基于 PoC 结果激活具有投票权重的新验证者集
**Epoch 过渡**：
- 有效 epoch 索引更新为即将到来的 epoch
- 对前一个 epoch 执行账户结算
- 为新 epoch 的参与者进行模型分配
- 为下一个验证期注册活跃参与者
## 权重系统和使用场景
Gonka 网络运行两种不同的权重系统，服务于不同的目的：
### 质押模块权重（共识权重）
此权重通过 `SetComputeValidators` 设置，用于所有 Cosmos SDK 原生共识和治理操作：
**使用场景**：
- **区块共识**：确定 CometBFT 中区块生产的验证者选择概率
- **治理投票**：链上治理提案的投票权重
- **惩罚**：影响验证者不当行为的惩罚幅度
- **验证者集**：控制哪些验证者在共识集中处于活跃状态
- **奖励分配**：影响区块奖励和佣金分配
**权重来源**：从 PoC 计算结果派生，在每个 epoch 验证周期结束时更新。
### EpochGroup 权重（内部网络权重）
此权重记录在 EpochGroups 及其子组中，用于 epoch 期间的内部网络操作：
**使用场景**：
- **PoC 验证决策**：确定对其他参与者 PoC 提交的基于多数的验证中的权重
- **推理工作分配**：控制每个参与者接收多少推理工作
- **模型分配**：影响参与者被分配服务哪些模型
- **网络资源分配**：影响 epoch 内计算资源的分配
- **参与者选择**：用于确定哪些参与者进入下一个 epoch
**权重来源**：基于历史 PoC 性能、从前一个 epoch 保留的权重以及 MLNode 计算能力。
### 权重流动和同步
两种权重系统在 epoch 生命周期的特定点同步：
1. **Epoch 期间**：EpochGroup 权重管理内部操作和 PoC 验证
2. **Epoch 结束时**：使用 EpochGroup 权重进行验证决策来计算 PoC 结果
3. **验证者更新**：成功参与者的权重通过 `SetComputeValidators` 转移到质押模块
4. **新 Epoch**：更新的共识权重变为活跃状态，同时建立新的 EpochGroup 权重
这种双权重架构确保区块链共识保持稳定和安全，同时允许在计算 epoch 期间灵活的资源分配和验证。
## 与修改版 Cosmos SDK 的集成
### SetComputeValidators 函数
PoC 结果和共识之间的核心集成点是修改的质押模块中的 `SetComputeValidators` 函数：
**函数位置**：在 `inference-chain/x/inference/types/expected_keepers.go` 中引用
**用途**：基于 PoC 计算结果而非质押代币更新验证者投票权重
**过程**：
1. 接收包含公钥和计算权重的 ComputeResult 对象
2. 将新结果与现有验证者集进行协调
3. 更新验证者权重索引以集成 CometBFT
4. 在不进行代币移动的情况下管理验证者过渡
### 权重计算覆盖
**传统质押**：投票权重 = 质押代币 / PowerReduction
**PoC 系统**：投票权重 = 计算的 PoC 分数（PowerReduction = 1）
`cosmos_changes.md` 中的修改系统确保：
- 基于 PoC 的验证者不进行实际的代币质押
- `TotalBondedTokens` 通过汇总验证者权重分数来计算
- 惩罚影响 PoC 分数而非销毁代币
- Hook 机制安全地触发抵押模块惩罚
## 关键实现文件
### 链侧 PoC 管理
- `inference-chain/x/inference/module/module.go` - 主要 epoch 和阶段管理
- `inference-chain/x/inference/module/chainvalidation.go` - PoC 验证逻辑
- `inference-chain/x/inference/keeper/msg_server_submit_poc_batch.go` - 批次提交处理器
- `inference-chain/x/inference/keeper/msg_server_submit_poc_validation.go` - 验证提交处理器
### API 节点编排
- `decentralized-api/internal/poc/node_orchestrator.go` - PoC 工作流协调
- `decentralized-api/broker/node_worker_commands.go` - ML 节点命令执行
- `decentralized-api/internal/event_listener/new_block_dispatcher.go` - 链事件处理
### ML 节点计算
- `mlnode/packages/pow/src/pow/compute/compute.py` - 核心 PoC 计算引擎
- `mlnode/packages/pow/src/pow/compute/worker.py` - 工作器进程管理
- `mlnode/packages/pow/src/pow/service/manager.py` - PoC 服务管理
## Epoch 和参与者管理
### Epoch 生命周期
- **创建**：在 PoC 开始时通过 `CreateEpochGroup` 创建新 epoch
- **追踪**：分别维护当前、即将到来和之前的 epoch
- **过渡**：epoch 准备和验证者切换之间有清晰的边界
- **存储**：Epoch 数据使用顺序索引和 PoC 起始区块高度存储
### 参与者选择
- **保留**：具有推理分配的前一个 epoch 参与者被保留
- **权重计算**：从 MLNode PoC 权重计算总权重
- **模型分配**：参与者接收推理工作的模型分配
- **注册**：基于 PoC 性能注册顶级矿工
这种设计创建了一个强大的计算共识机制，其中验证者投票权重直接反映已证明的计算能力而非经济质押，同时保持底层 Cosmos SDK 共识引擎的安全性和功能性。
