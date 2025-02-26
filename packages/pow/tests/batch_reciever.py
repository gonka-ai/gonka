from fastapi import FastAPI, HTTPException
from typing import Union
from pow.data import ProofBatch, ValidatedBatch

app = FastAPI()
app.state.validated_batch = []
app.state.proof_batch = []

@app.post("/generated")
async def receive_proof_batch(batch: ProofBatch):
    try:
        app.state.proof_batch.append(batch)
        return {"message": "Received ProofBatch successfully"}
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))

@app.post("/validated")
async def receive_validated_batch(batch: ValidatedBatch):
    try:
        app.state.validated_batch.append(batch)
        return {"message": "Received ValidatedBatch successfully"}
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))

@app.get("/generated")
async def get_proof_batches():
    return {"proof_batches": [batch.__dict__ for batch in app.state.proof_batch]}

@app.get("/validated")
async def get_validated_batches():
    return {"validated_batches": [batch.__dict__ for batch in app.state.validated_batch]}

@app.post("/clear_batches")
async def clear_batches():
    app.state.proof_batch.clear()
    app.state.validated_batch.clear()
    return {"message": "All batches cleared"}

