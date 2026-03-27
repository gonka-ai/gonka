# Models Proposal

This is a proposal to add the ... multimodal models to the Gonka inference network. 

Validation thresholds for all the models were computed using the standard procedure described in [visual_models/README.md](../README.md).

For each model, there are respective notebooks with the details of experiments and gdrive folders with raw inference-validation data:


| Parameter |  |  |  | |
|-----------|-----------|-------------|-------------------|-------------|
| Notebook | |  |  |  |
| Validation Data | |  |  |  |
| Model Len || |  |  |
| Validation Thresholds |  | |  |  |
| Fraud Accuracy |  | |  |  |
| Tested Against | || |  |
| VRAM (example setup) | | ||  |


For the reproduction of raw data, the inference script producing the raw data is here: [link](). You'll also need to set up configs in this script, you'll find them in GDrive with the raw data.

All experiments were conducted using MLNode vX.X.X.

**Qwen3-30B** is suggested to be deployed with with the following parameters:
```python
additional_args=[
    '--max-model-len', '100000', #Fits the minimum 48GB
    '--enable-auto-tool-choice',  # Optional: enables automatic tool choice
    '--tool-call-parser', 'hermes',  # Optional: specifies the Hermes tool call parser
]
```



